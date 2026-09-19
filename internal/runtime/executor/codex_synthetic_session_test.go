package executor

import (
	"testing"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// codexResponsesTurn builds a Responses payload whose input carries the turns
// so far. A conversation appends to input, so turn N contains turn N-1.
func codexResponsesTurn(texts ...string) []byte {
	body := `{"model":"gpt-5-codex","input":[`
	for i, text := range texts {
		if i > 0 {
			body += `,`
		}
		body += `{"role":"user","content":[{"type":"input_text","text":"` + text + `"}]}`
	}
	return []byte(body + `]}`)
}

func codexSyntheticCacheKey(t *testing.T, auth string, payload []byte) string {
	t.Helper()
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: payload}
	httpReq := codexCacheHelperRequest(t, codexSessionIsolationAuth(auth, "acct-"+auth, auth+"@example.com"),
		sdktranslator.FromString("openai-response"), req)
	return gjson.GetBytes(readRequestBody(t, httpReq), "prompt_cache_key").String()
}

// A Responses client that sends no prompt_cache_key must still get one, and it
// must be the same across the turns of one conversation. Without this the turns
// scatter across upstream prompt cache nodes and a growing prefix is re-read in
// full nearly every turn.
func TestCodexSynthesisesStablePromptCacheKeyAcrossTurns(t *testing.T) {
	turn1 := codexSyntheticCacheKey(t, "stable", codexResponsesTurn("refactor the parser"))
	turn2 := codexSyntheticCacheKey(t, "stable", codexResponsesTurn("refactor the parser", "now add tests"))
	turn3 := codexSyntheticCacheKey(t, "stable", codexResponsesTurn("refactor the parser", "now add tests", "and docs"))

	if turn1 == "" {
		t.Fatal("no prompt_cache_key was synthesised for a client that sent none")
	}
	if turn1 != turn2 || turn2 != turn3 {
		t.Fatalf("prompt_cache_key drifted across turns of one conversation: %q / %q / %q", turn1, turn2, turn3)
	}
	if _, err := uuid.Parse(turn1); err != nil {
		t.Fatalf("prompt_cache_key %q is not a UUID, unlike the one a real Codex client sends: %v", turn1, err)
	}
}

// Distinct conversations must not collapse onto one key: that would pin
// unrelated prefixes to a single cache node and mislead sticky selection.
func TestCodexSynthesisedPromptCacheKeyDiffersPerConversation(t *testing.T) {
	first := codexSyntheticCacheKey(t, "convo", codexResponsesTurn("refactor the parser"))
	second := codexSyntheticCacheKey(t, "convo", codexResponsesTurn("write a changelog"))

	if first == "" || second == "" {
		t.Fatalf("prompt_cache_key missing: %q / %q", first, second)
	}
	if first == second {
		t.Fatalf("two conversations shared one synthesised prompt_cache_key: %q", first)
	}
}

// The upstream prompt cache is per account, so the same conversation routed to
// a different account must not reuse the other account's key.
func TestCodexSynthesisedPromptCacheKeyIsScopedPerAccount(t *testing.T) {
	payload := codexResponsesTurn("refactor the parser")
	onA := codexSyntheticCacheKey(t, "acct-scope-a", payload)
	onB := codexSyntheticCacheKey(t, "acct-scope-b", payload)

	if onA == "" || onB == "" {
		t.Fatalf("prompt_cache_key missing: %q / %q", onA, onB)
	}
	if onA == onB {
		t.Fatalf("two accounts shared one synthesised prompt_cache_key: %q", onA)
	}
}

// A client that does send prompt_cache_key must keep going through the account
// scoping path, not the synthesis fallback.
func TestCodexKeepsScopingExplicitPromptCacheKey(t *testing.T) {
	payload := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"01a07aa5-28f0-7150-839e-aef60c274e3e","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	first := codexSyntheticCacheKey(t, "explicit", payload)
	second := codexSyntheticCacheKey(t, "explicit", payload)

	if first != second {
		t.Fatalf("explicit prompt_cache_key mapped inconsistently: %q vs %q", first, second)
	}
	if first == "01a07aa5-28f0-7150-839e-aef60c274e3e" {
		t.Fatal("explicit prompt_cache_key reached upstream unscoped")
	}
	if parsed, err := uuid.Parse(first); err != nil || parsed.Version() != 7 {
		t.Fatalf("scoped key %q should stay a v7 UUID like the client's (err=%v)", first, err)
	}
}

// A body with nothing session-like in it must not produce a key: a fabricated
// one would be per-request noise rather than a session identity.
func TestCodexSynthesisesNoKeyWithoutSessionEvidence(t *testing.T) {
	if key := codexSyntheticCacheKey(t, "empty", []byte(`{"model":"gpt-5-codex"}`)); key != "" {
		t.Fatalf("prompt_cache_key = %q, want none when the body carries no session evidence", key)
	}
}
