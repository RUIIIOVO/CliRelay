package responses

import (
	"bytes"
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// geminiFinishOutcome is how a Gemini candidate's finishReason is reported on
// the Responses side.
//
// Every finishReason used to become status "completed". A turn that Gemini
// ended with MALFORMED_FUNCTION_CALL, or with STOP and nothing but thoughts,
// then reached the client as a normal, empty reply: an agent client stopped
// after the thinking block and waited for the user, and never used its own
// retry because nothing had failed.
type geminiFinishOutcome struct {
	// Status is the Responses status: "completed", "incomplete" or "failed".
	Status string
	// IncompleteReason is set when Status is "incomplete".
	IncompleteReason string
	// ErrorCode and ErrorMessage are set when Status is "failed".
	ErrorCode    string
	ErrorMessage string
}

// geminiRetryHint is appended to failed-turn messages. The failures below are
// sampling outcomes, not request errors, so the same request usually succeeds
// on a second draw; the phrase is what agent clients (pi, Codex CLI) match to
// retry a turn on their own.
const geminiRetryHint = "please retry your request"

// geminiModelOutputErrorType must match responsesModelOutputErrorType in
// sdk/cliproxy/auth: a failure of what the model sampled, not of the account.
const geminiModelOutputErrorType = "model_output_error"

// classifyGeminiFinish maps a Gemini finishReason onto a Responses outcome.
// hasOutput reports whether the turn produced anything the client can act on:
// visible text, a function call or an image. Thoughts alone do not count.
func classifyGeminiFinish(finishReason, finishMessage string, hasOutput bool) geminiFinishOutcome {
	reason := strings.ToUpper(strings.TrimSpace(finishReason))
	switch reason {
	case "", "STOP", "FINISH_REASON_UNSPECIFIED":
		if hasOutput {
			return geminiFinishOutcome{Status: "completed"}
		}
		// A turn with no text and no tool call leaves an agent with nothing to
		// do next, which is never a useful reply.
		return geminiFailed("empty_response", fmt.Sprintf(
			"upstream model finished without any output (finishReason=%s); %s", nonEmpty(reason, "none"), geminiRetryHint), finishMessage)
	case "MAX_TOKENS":
		return geminiFinishOutcome{Status: "incomplete", IncompleteReason: "max_output_tokens"}
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
		return geminiFinishOutcome{Status: "incomplete", IncompleteReason: "content_filter"}
	default:
		// MALFORMED_FUNCTION_CALL, UNEXPECTED_TOOL_CALL, TOO_MANY_TOOL_CALLS,
		// MALFORMED_RESPONSE, OTHER, LANGUAGE and anything newer: the model
		// broke off the turn. Whatever was streamed before it is not a complete
		// reply, so it is reported as a failure the client can retry.
		return geminiFailed(strings.ToLower(reason), fmt.Sprintf(
			"upstream model stopped with finishReason=%s; %s", reason, geminiRetryHint), finishMessage)
	}
}

func geminiFailed(code, message, finishMessage string) geminiFinishOutcome {
	if detail := strings.TrimSpace(finishMessage); detail != "" {
		const maxDetail = 300
		if len(detail) > maxDetail {
			detail = detail[:maxDetail] + "..."
		}
		message += " (" + detail + ")"
	}
	return geminiFinishOutcome{Status: "failed", ErrorCode: code, ErrorMessage: message}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// apply writes the outcome onto a Responses object at prefix ("response." for
// a stream event, "" for a non-stream body).
func (o geminiFinishOutcome) apply(respJSON, prefix string) string {
	respJSON, _ = sjson.Set(respJSON, prefix+"status", o.Status)
	switch o.Status {
	case "incomplete":
		respJSON, _ = sjson.Set(respJSON, prefix+"incomplete_details.reason", o.IncompleteReason)
	case "failed":
		// error.type tells the stream completion tracker this is a model output
		// failure, so the upstream account is not penalised for it.
		respJSON, _ = sjson.Set(respJSON, prefix+"error.type", geminiModelOutputErrorType)
		respJSON, _ = sjson.Set(respJSON, prefix+"error.code", o.ErrorCode)
		respJSON, _ = sjson.Set(respJSON, prefix+"error.message", o.ErrorMessage)
	}
	return respJSON
}

// eventType is the terminal stream event for the outcome.
func (o geminiFinishOutcome) eventType() string {
	switch o.Status {
	case "incomplete":
		return "response.incomplete"
	case "failed":
		return "response.failed"
	default:
		return "response.completed"
	}
}

// logGeminiFinish records a turn that did not complete normally. The
// finishReason is otherwise invisible: request logs only show a successful
// request with zero output tokens.
func logGeminiFinish(modelName string, o geminiFinishOutcome, finishReason, finishMessage string, usage gjson.Result) {
	if o.Status == "completed" {
		return
	}
	log.WithFields(log.Fields{
		"model":           modelName,
		"finish_reason":   finishReason,
		"finish_message":  truncateForLog(finishMessage, 300),
		"status":          o.Status,
		"prompt_tokens":   usage.Get("promptTokenCount").Int(),
		"output_tokens":   usage.Get("candidatesTokenCount").Int(),
		"thoughts_tokens": usage.Get("thoughtsTokenCount").Int(),
	}).Warn("gemini responses: turn did not complete normally")
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// geminiStreamChunkToConvert decides what the stream converter processes for
// one raw chunk. It returns false when there is nothing to do.
//
// Antigravity can end a 200 stream without ever sending a finishReason: proto3
// omits FINISH_REASON_UNSPECIFIED, and it happens on turns that produced only
// thoughts (router-for-me/CLIProxyAPI#5226). Without a terminal event the
// client sees a dropped stream, so on [DONE] one is synthesized from the state
// seen so far. Executors only pass [DONE] for a stream that ended cleanly.
func geminiStreamChunkToConvert(st *geminiToResponsesState, rawJSON []byte) ([]byte, bool) {
	if len(rawJSON) == 0 {
		return nil, false
	}
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		if !st.Started || st.Finished {
			return nil, false
		}
		synthetic := `{"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED"}]}`
		if st.LastUsage != "" {
			synthetic, _ = sjson.SetRaw(synthetic, "usageMetadata", st.LastUsage)
		}
		return []byte(synthetic), true
	}
	// Anything after the terminal event would open a second response.
	if st.Finished {
		return nil, false
	}
	if um := unwrapGeminiResponseRoot(gjson.ParseBytes(rawJSON)).Get("usageMetadata"); um.Exists() {
		st.LastUsage = um.Raw
	}
	return rawJSON, true
}

// finishGeminiStreamResponse turns the assembled response.completed payload
// into the terminal event the finishReason calls for.
func finishGeminiStreamResponse(st *geminiToResponsesState, modelName string, root gjson.Result, completed string) string {
	finishReason := root.Get("candidates.0.finishReason").String()
	finishMessage := root.Get("candidates.0.finishMessage").String()
	hasOutput := st.MsgOpened || len(st.FuncCallIDs) > 0 || len(st.ImageItems) > 0
	outcome := classifyGeminiFinish(finishReason, finishMessage, hasOutput)
	logGeminiFinish(modelName, outcome, finishReason, finishMessage, root.Get("usageMetadata"))
	completed = outcome.apply(completed, "response.")
	completed, _ = sjson.Set(completed, "type", outcome.eventType())
	return emitEvent(outcome.eventType(), completed)
}

// finishGeminiResponse applies the finishReason outcome to a non-stream body.
func finishGeminiResponse(modelName string, root gjson.Result, resp string, hasOutput bool) string {
	finishReason := root.Get("candidates.0.finishReason").String()
	finishMessage := root.Get("candidates.0.finishMessage").String()
	outcome := classifyGeminiFinish(finishReason, finishMessage, hasOutput)
	logGeminiFinish(modelName, outcome, finishReason, finishMessage, root.Get("usageMetadata"))
	return outcome.apply(resp, "")
}
