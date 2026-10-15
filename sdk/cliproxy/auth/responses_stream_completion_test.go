package auth

import (
	"net/http"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestResponsesStreamCompletionTrackerAcceptsCompletedWithoutSSEDelimiter(t *testing.T) {
	t.Parallel()

	tracker := newResponsesStreamCompletionTracker(sdktranslator.FormatOpenAIResponse)
	tracker.Observe([]byte("event: response.created\ndata: {\"type\":\"response.created\"}"))
	tracker.Observe([]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}"))

	if err := tracker.ErrIfIncomplete(); err != nil {
		t.Fatalf("ErrIfIncomplete() error = %v, want nil", err)
	}
}

func TestResponsesStreamCompletionTrackerAcceptsDataOnlyCompletedChunk(t *testing.T) {
	t.Parallel()

	tracker := newResponsesStreamCompletionTracker(sdktranslator.FormatOpenAIResponse)
	tracker.Observe([]byte("data: {\"type\":\"response.created\"}"))
	tracker.Observe([]byte("data: {\"type\":\"response.completed\"}"))

	if err := tracker.ErrIfIncomplete(); err != nil {
		t.Fatalf("ErrIfIncomplete() error = %v, want nil", err)
	}
}

func TestResponsesStreamCompletionTrackerReturnsResponseFailedError(t *testing.T) {
	t.Parallel()

	tracker := newResponsesStreamCompletionTracker(sdktranslator.FormatOpenAIResponse)
	tracker.Observe([]byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"Rate limit reached\"}}}"))

	err := tracker.ErrIfIncomplete()
	if err == nil {
		t.Fatal("ErrIfIncomplete() error = nil, want response.failed error")
	}
	if strings.Contains(err.Error(), "response.completed") {
		t.Fatalf("ErrIfIncomplete() error = %v, should preserve upstream failure instead of incomplete stream", err)
	}
	if !strings.Contains(err.Error(), "Rate limit reached") {
		t.Fatalf("ErrIfIncomplete() error = %v, want upstream message", err)
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %v/%v, want %d", statusErr, ok, http.StatusTooManyRequests)
	}
}

func TestResponsesStreamCompletionTrackerReturnsResponseFailedAfterPriorEvents(t *testing.T) {
	t.Parallel()

	tracker := newResponsesStreamCompletionTracker(sdktranslator.FormatOpenAIResponse)
	tracker.Observe([]byte("event: response.created\ndata: {\"type\":\"response.created\"}\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\nevent: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"upstream failed after delta\"}}}"))

	err := tracker.ErrIfIncomplete()
	if err == nil {
		t.Fatal("ErrIfIncomplete() error = nil, want response.failed error")
	}
	if strings.Contains(err.Error(), "response.completed") {
		t.Fatalf("ErrIfIncomplete() error = %v, should preserve upstream failure instead of incomplete stream", err)
	}
	if !strings.Contains(err.Error(), "upstream failed after delta") {
		t.Fatalf("ErrIfIncomplete() error = %v, want upstream message", err)
	}
}

func TestResponsesStreamCompletionTrackerReturnsTopLevelError(t *testing.T) {
	t.Parallel()

	tracker := newResponsesStreamCompletionTracker(sdktranslator.FormatOpenAIResponse)
	tracker.Observe([]byte("event: error\ndata: {\"type\":\"error\",\"code\":\"internal_server_error\",\"message\":\"upstream exploded\"}"))

	err := tracker.ErrIfIncomplete()
	if err == nil {
		t.Fatal("ErrIfIncomplete() error = nil, want top-level error")
	}
	if strings.Contains(err.Error(), "response.completed") {
		t.Fatalf("ErrIfIncomplete() error = %v, should preserve upstream error instead of incomplete stream", err)
	}
	if !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("ErrIfIncomplete() error = %v, want upstream message", err)
	}
}

func TestResponsesStreamCompletionTrackerRejectsTrueIncompleteStream(t *testing.T) {
	t.Parallel()

	tracker := newResponsesStreamCompletionTracker(sdktranslator.FormatOpenAIResponse)
	tracker.Observe([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}"))

	err := tracker.ErrIfIncomplete()
	if err == nil {
		t.Fatal("ErrIfIncomplete() error = nil, want incomplete stream error")
	}
	if !strings.Contains(err.Error(), "response.completed") {
		t.Fatalf("ErrIfIncomplete() error = %v, want incomplete stream message", err)
	}
}

// response.incomplete (max_output_tokens, content_filter) ends the stream
// normally; it must not be reported as a dropped stream and penalise the account.
func TestResponsesStreamCompletionTrackerAcceptsIncomplete(t *testing.T) {
	t.Parallel()

	tracker := newResponsesStreamCompletionTracker(sdktranslator.FormatOpenAIResponse)
	tracker.Observe([]byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
	tracker.Observe([]byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"))

	if err := tracker.ErrIfIncomplete(); err != nil {
		t.Fatalf("ErrIfIncomplete() error = %v, want nil", err)
	}
}

// A failure of what the model sampled (Gemini MALFORMED_FUNCTION_CALL, an empty
// turn) is not an upstream fault and must not count against the credential.
func TestResponsesStreamCompletionTrackerAcceptsModelOutputFailure(t *testing.T) {
	t.Parallel()

	for _, chunk := range []string{
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"model_output_error\",\"code\":\"malformed_function_call\",\"message\":\"upstream model stopped with finishReason=MALFORMED_FUNCTION_CALL; please retry your request\"}}}\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"model_output_error\",\"code\":\"empty_response\",\"message\":\"x\"}}}\n\n",
	} {
		tracker := newResponsesStreamCompletionTracker(sdktranslator.FormatOpenAIResponse)
		tracker.Observe([]byte(chunk))
		if err := tracker.ErrIfIncomplete(); err != nil {
			t.Fatalf("ErrIfIncomplete() error = %v, want nil for %q", err, chunk)
		}
	}
}
