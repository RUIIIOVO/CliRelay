package modelconfig

import (
	"testing"
)

// Disabling a model in the catalog used to be a no-op for anything the static
// registry already served: every Enabled check in the codebase was additive
// ("should this row contribute a model?"), so a disabled row simply stopped
// contributing a model that was being supplied from elsewhere anyway. These
// cover the subtractive lookup the listing and request paths now rely on.

func seedConfig(t *testing.T, modelID string, enabled bool) {
	t.Helper()
	if _, err := UpsertConfig(UpsertConfigInput{
		ModelID:     modelID,
		Scope:       "active",
		OwnedBy:     "anthropic",
		Enabled:     enabled,
		PricingMode: "call",
	}); err != nil {
		t.Fatalf("UpsertConfig(%q, enabled=%v) error = %v", modelID, enabled, err)
	}
}

func TestDisabledModelIDsCollectsOnlyDisabledRows(t *testing.T) {
	initModelConfigServiceTestDB(t)

	seedConfig(t, "claude-opus-4-6", false)
	seedConfig(t, "claude-opus-5", true)
	seedConfig(t, "claude-sonnet-4-6", false)

	disabled := DisabledModelIDs()

	if len(disabled) != 2 {
		t.Fatalf("DisabledModelIDs() size = %d, want 2 (got %v)", len(disabled), disabled)
	}
	for _, want := range []string{"claude-opus-4-6", "claude-sonnet-4-6"} {
		if _, ok := disabled[want]; !ok {
			t.Errorf("DisabledModelIDs() missing %q", want)
		}
	}
	if _, ok := disabled["claude-opus-5"]; ok {
		t.Error("DisabledModelIDs() contains an enabled model")
	}
}

func TestDisabledModelIDsIsEmptyWhenNothingDisabled(t *testing.T) {
	initModelConfigServiceTestDB(t)

	seedConfig(t, "claude-opus-5", true)

	if disabled := DisabledModelIDs(); len(disabled) != 0 {
		t.Fatalf("DisabledModelIDs() = %v, want empty", disabled)
	}
}

func TestDisabledModelIDsKeysAreCaseFolded(t *testing.T) {
	initModelConfigServiceTestDB(t)

	seedConfig(t, "Claude-Opus-4-6", false)

	disabled := DisabledModelIDs()
	if _, ok := disabled[NormalizeModelKey("claude-OPUS-4-6")]; !ok {
		t.Fatalf("DisabledModelIDs() = %v, want a case-folded key", disabled)
	}
}

func TestIsModelDisabledForTenant(t *testing.T) {
	initModelConfigServiceTestDB(t)

	seedConfig(t, "claude-opus-4-6", false)
	seedConfig(t, "claude-opus-5", true)

	cases := []struct {
		name  string
		model string
		want  bool
	}{
		{"disabled row", "claude-opus-4-6", true},
		{"enabled row", "claude-opus-5", false},
		{"unknown model is not disabled", "gpt-5", false},
		{"blank model is not disabled", "   ", false},
		{"lookup tolerates surrounding space", "  claude-opus-4-6  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsModelDisabledForTenant("", tc.model); got != tc.want {
				t.Errorf("IsModelDisabledForTenant(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestNormalizeModelKey(t *testing.T) {
	cases := map[string]string{
		"  Claude-Opus-4-6 ": "claude-opus-4-6",
		"CLAUDE-OPUS-5":      "claude-opus-5",
		"":                   "",
		"   ":                "",
	}
	for input, want := range cases {
		if got := NormalizeModelKey(input); got != want {
			t.Errorf("NormalizeModelKey(%q) = %q, want %q", input, got, want)
		}
	}
}
