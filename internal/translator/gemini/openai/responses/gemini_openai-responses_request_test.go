package responses

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/tidwall/gjson"
)

// Long enough to pass cache.MinValidSignatureLen, which CacheSignature enforces.
const reasoningReplaySignature = "RXE0RENrZ0lDeEFDR0FJcVFOZDdjUzlleGFuRktRdFcvSzNyZ2MvWDNCcDQ4RmxSbGxOWUlOVU5kR1l1UHMrMGdkMVp0Vkg3ekdKU0g4YVljc2JjN3lNK0FrdGpTNUdqamI4T3Z0VVNETzdQd3pmcFhUOGl3U3hXUEJvTVFRQ09mWTFyMEtTWGZxUUlJakFqdmFGWk83RW1XRlBKckJVOVpkYzdDKw=="

func reasoningReplayRequest(modelName, thinkingText, encryptedContent string) []byte {
	return []byte(`{
		"model": "` + modelName + `",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "` + thinkingText + `"}], "content": [], "encrypted_content": "` + encryptedContent + `"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hello"}]}
		]
	}`)
}

func thoughtParts(raw []byte) []gjson.Result {
	var parts []gjson.Result
	gjson.GetBytes(raw, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("thought").Bool() {
				parts = append(parts, part)
			}
			return true
		})
		return true
	})
	return parts
}

// Codex replays its own reasoning items with an empty encrypted_content. An
// unsigned thinking part is rejected by the Antigravity Claude models with
// `messages.N.content.M.thinking.signature: Field required`, so it must not be
// forwarded at all.
func TestReasoningReplayDropsUnsignedThoughtPart(t *testing.T) {
	const thinkingText = "unsigned-reasoning-replay-must-be-dropped"
	out := ConvertOpenAIResponsesRequestToGemini(
		"claude-opus-4-6-thinking",
		reasoningReplayRequest("claude-opus-4-6-thinking", thinkingText, ""),
		true,
	)
	if parts := thoughtParts(out); len(parts) != 0 {
		t.Fatalf("unsigned thinking part was forwarded: %#v", parts)
	}
	// The rest of the conversation still has to be translated.
	if got := gjson.GetBytes(out, "contents.#").Int(); got != 2 {
		t.Fatalf("contents = %d, want the user and assistant messages only", got)
	}
}

// The response side caches the upstream signature for the thinking text, which is
// what makes a signed replay possible when the client cannot supply one.
func TestReasoningReplayUsesCachedSignature(t *testing.T) {
	const thinkingText = "cached-signature-reasoning-replay"
	cache.CacheSignature("claude-opus-4-6-thinking", thinkingText, reasoningReplaySignature)

	out := ConvertOpenAIResponsesRequestToGemini(
		"claude-opus-4-6-thinking",
		reasoningReplayRequest("claude-opus-4-6-thinking", thinkingText, ""),
		true,
	)
	parts := thoughtParts(out)
	if len(parts) != 1 {
		t.Fatalf("thinking parts = %d, want 1", len(parts))
	}
	if got := parts[0].Get("thoughtSignature").String(); got != reasoningReplaySignature {
		t.Fatalf("thoughtSignature = %q, want the cached signature", got)
	}
	if got := parts[0].Get("text").String(); got != thinkingText {
		t.Fatalf("thought text = %q, want %q", got, thinkingText)
	}
}

// A client that does supply a signature keeps winning over the cache.
func TestReasoningReplayPrefersClientSignature(t *testing.T) {
	const thinkingText = "client-signature-wins"
	clientSignature := reasoningReplaySignature + "client"
	cache.CacheSignature("claude-opus-4-6-thinking", thinkingText, reasoningReplaySignature)

	out := ConvertOpenAIResponsesRequestToGemini(
		"claude-opus-4-6-thinking",
		reasoningReplayRequest("claude-opus-4-6-thinking", thinkingText, clientSignature),
		true,
	)
	parts := thoughtParts(out)
	if len(parts) != 1 {
		t.Fatalf("thinking parts = %d, want 1", len(parts))
	}
	if got := parts[0].Get("thoughtSignature").String(); got != clientSignature {
		t.Fatalf("thoughtSignature = %q, want the client signature %q", got, clientSignature)
	}
}

// Gemini is the one family with a sanctioned escape hatch, and the cache returns
// it on a miss — so Gemini replays keep their thinking part unchanged.
func TestReasoningReplayKeepsGeminiThinkingPartViaSentinel(t *testing.T) {
	const thinkingText = "gemini-replay-keeps-thinking-part"
	out := ConvertOpenAIResponsesRequestToGemini(
		"gemini-3.1-pro-low",
		reasoningReplayRequest("gemini-3.1-pro-low", thinkingText, ""),
		true,
	)
	parts := thoughtParts(out)
	if len(parts) != 1 {
		t.Fatalf("thinking parts = %d, want 1", len(parts))
	}
	if got := parts[0].Get("thoughtSignature").String(); !strings.Contains(got, "skip_thought_signature_validator") {
		t.Fatalf("thoughtSignature = %q, want the Gemini sentinel", got)
	}
}
