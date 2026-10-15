package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const (
	geminiThoughtChunk = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"Next I will write the config file.","thoughtSignature":"sig_abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnop"}]}}],"responseId":"req_finish"}}`
	geminiTextChunk    = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]}}],"responseId":"req_finish"}}`
	geminiCallChunk    = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"command":"ls"}}}]}}],"responseId":"req_finish"}}`
)

func geminiFinishChunk(reason, message string) string {
	c := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"` + reason + `"`
	if message != "" {
		c += `,"finishMessage":"` + message + `"`
	}
	return c + `}],"usageMetadata":{"promptTokenCount":180000,"candidatesTokenCount":0,"thoughtsTokenCount":68,"totalTokenCount":180068},"responseId":"req_finish"}}`
}

// runGeminiStream returns every event name in order and the terminal event.
func runGeminiStream(t *testing.T, chunks ...string) ([]string, string, gjson.Result) {
	t.Helper()
	req := []byte(`{"model":"gemini-3.8-flash-high","input":"hi"}`)
	var param any
	var names []string
	var lastName string
	var last gjson.Result
	for _, chunk := range chunks {
		for _, ev := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", req, nil, []byte(chunk), &param) {
			name, data := parseSSEEvent(t, ev)
			names = append(names, name)
			lastName, last = name, data
		}
	}
	return names, lastName, last
}

func TestGeminiResponsesStream_FinishOutcome(t *testing.T) {
	cases := []struct {
		name       string
		chunks     []string
		wantEvent  string
		wantStatus string
		check      func(t *testing.T, resp gjson.Result)
	}{
		{
			// The production case: thoughts, then STOP with nothing else. It used
			// to reach the client as a normal, empty reply.
			name:       "thoughts only then STOP",
			chunks:     []string{geminiThoughtChunk, geminiFinishChunk("STOP", "")},
			wantEvent:  "response.failed",
			wantStatus: "failed",
			check: func(t *testing.T, resp gjson.Result) {
				if got := resp.Get("error.code").String(); got != "empty_response" {
					t.Errorf("error.code = %q, want empty_response", got)
				}
			},
		},
		{
			name:       "thoughts then MALFORMED_FUNCTION_CALL",
			chunks:     []string{geminiThoughtChunk, geminiFinishChunk("MALFORMED_FUNCTION_CALL", "Malformed function call: bash")},
			wantEvent:  "response.failed",
			wantStatus: "failed",
			check: func(t *testing.T, resp gjson.Result) {
				if got := resp.Get("error.code").String(); got != "malformed_function_call" {
					t.Errorf("error.code = %q, want malformed_function_call", got)
				}
				if msg := resp.Get("error.message").String(); !strings.Contains(msg, "MALFORMED_FUNCTION_CALL") || !strings.Contains(msg, "Malformed function call: bash") {
					t.Errorf("error.message = %q, want the finishReason and finishMessage", msg)
				}
			},
		},
		{
			name:       "text then UNEXPECTED_TOOL_CALL",
			chunks:     []string{geminiTextChunk, geminiFinishChunk("UNEXPECTED_TOOL_CALL", "")},
			wantEvent:  "response.failed",
			wantStatus: "failed",
		},
		{
			name:       "text then STOP",
			chunks:     []string{geminiThoughtChunk, geminiTextChunk, geminiFinishChunk("STOP", "")},
			wantEvent:  "response.completed",
			wantStatus: "completed",
		},
		{
			name:       "function call then STOP",
			chunks:     []string{geminiThoughtChunk, geminiCallChunk, geminiFinishChunk("STOP", "")},
			wantEvent:  "response.completed",
			wantStatus: "completed",
		},
		{
			name:       "MAX_TOKENS",
			chunks:     []string{geminiTextChunk, geminiFinishChunk("MAX_TOKENS", "")},
			wantEvent:  "response.incomplete",
			wantStatus: "incomplete",
			check: func(t *testing.T, resp gjson.Result) {
				if got := resp.Get("incomplete_details.reason").String(); got != "max_output_tokens" {
					t.Errorf("incomplete_details.reason = %q, want max_output_tokens", got)
				}
			},
		},
		{
			name:       "SAFETY",
			chunks:     []string{geminiTextChunk, geminiFinishChunk("SAFETY", "")},
			wantEvent:  "response.incomplete",
			wantStatus: "incomplete",
			check: func(t *testing.T, resp gjson.Result) {
				if got := resp.Get("incomplete_details.reason").String(); got != "content_filter" {
					t.Errorf("incomplete_details.reason = %q, want content_filter", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names, lastName, last := runGeminiStream(t, tc.chunks...)
			if lastName != tc.wantEvent {
				t.Fatalf("terminal event = %q, want %q (events %v)", lastName, tc.wantEvent, names)
			}
			if got := last.Get("type").String(); got != tc.wantEvent {
				t.Errorf("payload type = %q, want %q", got, tc.wantEvent)
			}
			resp := last.Get("response")
			if got := resp.Get("status").String(); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
			if tc.wantStatus == "failed" {
				// Marks a model-output failure so the account is not penalised,
				// and carries the phrase agent clients retry on.
				if got := resp.Get("error.type").String(); got != geminiModelOutputErrorType {
					t.Errorf("error.type = %q, want %q", got, geminiModelOutputErrorType)
				}
				if msg := resp.Get("error.message").String(); !strings.Contains(msg, geminiRetryHint) {
					t.Errorf("error.message = %q, want it to contain %q", msg, geminiRetryHint)
				}
			}
			if tc.check != nil {
				tc.check(t, resp)
			}
			// Exactly one terminal event.
			terminal := 0
			for _, n := range names {
				switch n {
				case "response.completed", "response.incomplete", "response.failed":
					terminal++
				}
			}
			if terminal != 1 {
				t.Errorf("got %d terminal events, want 1 (%v)", terminal, names)
			}
		})
	}
}

// The reasoning item is still closed and listed on a failed turn, so the client
// sees the thoughts the model produced before it broke off.
func TestGeminiResponsesStream_FailedTurnKeepsReasoning(t *testing.T) {
	names, _, last := runGeminiStream(t, geminiThoughtChunk, geminiFinishChunk("STOP", ""))
	sawReasoningDone := false
	for _, n := range names {
		if n == "response.reasoning_summary_text.done" {
			sawReasoningDone = true
		}
	}
	if !sawReasoningDone {
		t.Fatalf("reasoning was not finalized before the failed event: %v", names)
	}
	if got := last.Get(`response.output.#(type=="reasoning").summary.0.text`).String(); got == "" {
		t.Fatalf("failed response lost its reasoning item: %s", last.Raw)
	}
}

func TestGeminiResponsesNonStream_FinishOutcome(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus string
	}{
		{"thoughts only then STOP", `{"candidates":[{"content":{"parts":[{"thought":true,"text":"plan"}]},"finishReason":"STOP"}]}`, "failed"},
		{"MALFORMED_FUNCTION_CALL", `{"candidates":[{"content":{"parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"bad call"}]}`, "failed"},
		{"text then STOP", `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`, "completed"},
		{"function call then STOP", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{}}}]},"finishReason":"STOP"}]}`, "completed"},
		{"MAX_TOKENS", `{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}]}`, "incomplete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := gjson.Parse(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.8-flash-high", nil, nil, []byte(tc.body), nil))
			if got := out.Get("status").String(); got != tc.wantStatus {
				t.Fatalf("status = %q, want %q: %s", got, tc.wantStatus, out.Raw)
			}
			if tc.wantStatus == "failed" && out.Get("error.type").String() != geminiModelOutputErrorType {
				t.Fatalf("error.type = %q, want %q", out.Get("error.type").String(), geminiModelOutputErrorType)
			}
		})
	}
}

// Antigravity can end a stream without any finishReason (proto3 omits
// FINISH_REASON_UNSPECIFIED), typically on a thoughts-only turn. A terminal
// event is synthesized on [DONE] from what the stream carried.
func TestGeminiResponsesStream_SynthesizesTerminalOnDone(t *testing.T) {
	usageChunk := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":" more"}]}}],"usageMetadata":{"promptTokenCount":170000,"thoughtsTokenCount":258,"totalTokenCount":170258},"responseId":"req_finish"}}`

	t.Run("thoughts only", func(t *testing.T) {
		names, lastName, last := runGeminiStream(t, geminiThoughtChunk, usageChunk, "[DONE]")
		if lastName != "response.failed" {
			t.Fatalf("terminal event = %q, want response.failed (%v)", lastName, names)
		}
		resp := last.Get("response")
		if got := resp.Get("error.code").String(); got != "empty_response" {
			t.Errorf("error.code = %q, want empty_response", got)
		}
		if got := resp.Get("error.type").String(); got != geminiModelOutputErrorType {
			t.Errorf("error.type = %q, want %q", got, geminiModelOutputErrorType)
		}
		if got := resp.Get("usage.output_tokens_details.reasoning_tokens").Int(); got != 258 {
			t.Errorf("reasoning_tokens = %d, want the last usage snapshot (258)", got)
		}
	})

	t.Run("text", func(t *testing.T) {
		names, lastName, _ := runGeminiStream(t, geminiTextChunk, "[DONE]")
		if lastName != "response.completed" {
			t.Fatalf("terminal event = %q, want response.completed (%v)", lastName, names)
		}
	})

	t.Run("finishReason already sent", func(t *testing.T) {
		names, _, _ := runGeminiStream(t, geminiTextChunk, geminiFinishChunk("STOP", ""), "[DONE]", geminiTextChunk)
		terminal := 0
		for _, n := range names {
			if n == "response.completed" || n == "response.failed" || n == "response.incomplete" {
				terminal++
			}
		}
		if terminal != 1 {
			t.Fatalf("got %d terminal events, want 1 (%v)", terminal, names)
		}
		if names[len(names)-1] != "response.completed" {
			t.Fatalf("events after the terminal one: %v", names)
		}
	})

	t.Run("nothing streamed", func(t *testing.T) {
		if names, _, _ := runGeminiStream(t, "[DONE]"); len(names) != 0 {
			t.Fatalf("got events for an empty stream: %v", names)
		}
	})
}
