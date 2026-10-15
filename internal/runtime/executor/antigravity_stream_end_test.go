package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

const antigravityThoughtOnlySSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"Next I will write the file."}]}}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":5,"totalTokenCount":15},"responseId":"r1"}}` + "\n\n"

func collectAntigravityResponsesStream(t *testing.T, handler http.HandlerFunc) (events []string, streamErr error) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()

	result, err := NewAntigravityExecutor(&config.Config{}).ExecuteStream(context.Background(), antigravityTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash-high",
		Payload: []byte(`{"model":"gemini-3.7-flash-high","stream":true,"input":"hi"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				return events, streamErr
			}
			if chunk.Err != nil {
				streamErr = chunk.Err
				continue
			}
			head, _, _ := strings.Cut(string(chunk.Payload), "\n")
			events = append(events, strings.TrimSpace(strings.TrimPrefix(head, "event:")))
		case <-timeout:
			t.Fatal("timed out reading the stream")
		}
	}
}

func terminalResponsesEvents(events []string) []string {
	var out []string
	for _, e := range events {
		switch e {
		case "response.completed", "response.incomplete", "response.failed":
			out = append(out, e)
		}
	}
	return out
}

// A thoughts-only stream that ends without a finishReason is a finished turn
// with no output: the translator synthesizes a retryable failure on [DONE].
func TestAntigravityStream_CleanEndWithoutFinishReasonIsReported(t *testing.T) {
	events, streamErr := collectAntigravityResponsesStream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(antigravityThoughtOnlySSE))
	})
	if streamErr != nil {
		t.Fatalf("stream error = %v, want none", streamErr)
	}
	if got := terminalResponsesEvents(events); len(got) != 1 || got[0] != "response.failed" {
		t.Fatalf("terminal events = %v, want [response.failed] (events %v)", got, events)
	}
}

// A connection that drops mid-stream is not a finished turn: the client must get
// the read error, not a synthesized terminal event that makes a partial reply
// look complete.
func TestAntigravityStream_ReadErrorIsNotReportedAsFinished(t *testing.T) {
	events, streamErr := collectAntigravityResponsesStream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "100000") // promise more than is sent
		_, _ = w.Write([]byte(antigravityThoughtOnlySSE))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	})
	if streamErr == nil {
		t.Fatalf("stream error = nil, want the read error (events %v)", events)
	}
	if got := terminalResponsesEvents(events); len(got) != 0 {
		t.Fatalf("terminal events = %v, want none after a read error", got)
	}
}
