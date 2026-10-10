package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const malformedFinish = `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: Function call is empty - no input to parse."}],"usageMetadata":{"promptTokenCount":10,"totalTokenCount":20,"thoughtsTokenCount":10},"responseId":"r1"}`

func terminalEvents(t *testing.T, chunks ...string) []string {
	t.Helper()
	var param any
	var terminal []string
	for _, chunk := range chunks {
		for _, event := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", nil, nil, []byte(chunk), &param) {
			eventType := strings.TrimPrefix(strings.SplitN(event, "\n", 2)[0], "event: ")
			if eventType == "response.completed" || eventType == "response.failed" {
				terminal = append(terminal, event)
			}
		}
	}
	return terminal
}

// A turn that ends in MALFORMED_FUNCTION_CALL with only reasoning is a failure.
// Reported as completed, agent harnesses end the turn and wait for the user.
func TestMalformedFunctionCallWithOnlyReasoningFails(t *testing.T) {
	thought := `{"candidates":[{"content":{"role":"model","parts":[{"text":"next I will run git","thought":true}]}}],"responseId":"r1"}`
	terminal := terminalEvents(t, thought, malformedFinish)
	if len(terminal) != 1 || !strings.HasPrefix(terminal[0], "event: response.failed") {
		t.Fatalf("terminal events = %v, want one response.failed", terminal)
	}
	payload := strings.TrimPrefix(strings.SplitN(terminal[0], "\n", 2)[1], "data: ")
	if status := gjson.Get(payload, "response.status").String(); status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if msg := gjson.Get(payload, "response.error.message").String(); !strings.Contains(msg, "MALFORMED_FUNCTION_CALL") || !strings.Contains(msg, "retry") {
		t.Fatalf("error message = %q", msg)
	}
}

// Text already delivered is output the client can act on; the turn stands.
func TestMalformedFunctionCallAfterTextCompletes(t *testing.T) {
	text := `{"candidates":[{"content":{"role":"model","parts":[{"text":"Here is the summary."}]}}],"responseId":"r1"}`
	terminal := terminalEvents(t, text, malformedFinish)
	if len(terminal) != 1 || !strings.HasPrefix(terminal[0], "event: response.completed") {
		t.Fatalf("terminal events = %v, want one response.completed", terminal)
	}
}

func TestMalformedFunctionCallNonStreamFails(t *testing.T) {
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.8-flash-high", nil, nil, []byte(malformedFinish), nil)
	if status := gjson.Get(out, "status").String(); status != "failed" {
		t.Fatalf("status = %q, want failed: %s", status, out)
	}
	if !strings.Contains(gjson.Get(out, "error.message").String(), "MALFORMED_FUNCTION_CALL") {
		t.Fatalf("error = %s", gjson.Get(out, "error").Raw)
	}
}
