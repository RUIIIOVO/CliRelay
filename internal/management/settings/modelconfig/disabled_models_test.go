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

// The listing filters and the request path used to carry a copy each of "is this
// model disabled", and the copies disagreed: only the request path understood
// prefixed aliases. A disabled "gpt-oss:20b" therefore stayed listed as
// "ollama/gpt-oss:20b" while every call to that alias was refused. They now share
// IsDisabled, so this asserts all three surfaces agree.
func TestDisabledPredicateIsSharedAcrossSurfaces(t *testing.T) {
	initModelConfigServiceTestDB(t)

	seedConfig(t, "gpt-oss:20b", false)
	seedConfig(t, "claude-opus-5", true)

	disabled := DisabledModelIDs()
	alias := "ollama/gpt-oss:20b"

	if !IsDisabled(alias, disabled) {
		t.Fatalf("IsDisabled(%q) = false, want true via the bare model id", alias)
	}

	listing := FilterOutDisabled("", []map[string]any{
		{"id": alias},
		{"id": "gpt-oss:20b"},
		{"id": "claude-opus-5"},
	})
	for _, model := range listing {
		if id, _ := model["id"].(string); id != "claude-opus-5" {
			t.Errorf("FilterOutDisabled kept %q, but the request path refuses it", id)
		}
	}

	ids := FilterOutDisabledIDs("", map[string]struct{}{
		alias:           {},
		"gpt-oss:20b":   {},
		"claude-opus-5": {},
	})
	if _, present := ids[alias]; present {
		t.Errorf("FilterOutDisabledIDs kept %q, but the request path refuses it", alias)
	}
	if _, present := ids["claude-opus-5"]; !present {
		t.Error("FilterOutDisabledIDs dropped an enabled model")
	}
}

// A write has to be visible to the next read on this replica, even though the
// set is cached for the hot path.
func TestDisabledModelCacheReflectsWritesImmediately(t *testing.T) {
	initModelConfigServiceTestDB(t)

	seedConfig(t, "claude-opus-4-6", false)
	if _, off := DisabledModelIDs()["claude-opus-4-6"]; !off {
		t.Fatal("freshly disabled model is not in the set")
	}

	seedConfig(t, "claude-opus-4-6", true)
	if _, off := DisabledModelIDs()["claude-opus-4-6"]; off {
		t.Fatal("re-enabled model still reported disabled; the cache was not invalidated after the write")
	}

	seedConfig(t, "claude-opus-4-6", false)
	if err := DeleteConfig("claude-opus-4-6"); err != nil {
		t.Fatalf("DeleteConfig error = %v", err)
	}
	if _, off := DisabledModelIDs()["claude-opus-4-6"]; off {
		t.Fatal("deleted model still reported disabled; the cache was not invalidated after the delete")
	}
}

// Each wrapper ("ollama/x", "cline-pass/x") is its own model_configs row with its
// own toggle. Disabling the bare model cascades to the wrappers because they
// route to it; disabling one wrapper must not switch off the bare model or the
// other wrappers.
func TestDisabledCascadeIsOneWayFromBareModelToWrappers(t *testing.T) {
	initModelConfigServiceTestDB(t)

	seedConfig(t, "ollama/gpt-oss:20b", false)
	seedConfig(t, "gpt-oss:20b", true)
	disabled := DisabledModelIDs()

	if !IsDisabled("ollama/gpt-oss:20b", disabled) {
		t.Fatal("the disabled wrapper itself is not reported disabled")
	}
	for _, id := range []string{"gpt-oss:20b", "cline-pass/gpt-oss:20b"} {
		if IsDisabled(id, disabled) {
			t.Errorf("IsDisabled(%q) = true; disabling one wrapper must not cascade to %q", id, id)
		}
	}
}

// The loader refuses to publish a result whose SELECT started before the last
// invalidation (it compares disabledCacheGen before and after). That guard is
// only sound if every write path bumps the generation, which is what this pins.
func TestDisabledModelCacheWritesBumpGeneration(t *testing.T) {
	initModelConfigServiceTestDB(t)

	readGen := func() uint64 {
		disabledCacheMu.RLock()
		defer disabledCacheMu.RUnlock()
		return disabledCacheGen
	}

	before := readGen()
	seedConfig(t, "claude-opus-4-6", false)
	if after := readGen(); after == before {
		t.Fatal("upsert did not bump the cache generation; an in-flight load could publish stale rows over it")
	}

	before = readGen()
	if err := DeleteConfig("claude-opus-4-6"); err != nil {
		t.Fatalf("DeleteConfig error = %v", err)
	}
	if after := readGen(); after == before {
		t.Fatal("delete did not bump the cache generation; an in-flight load could publish stale rows over it")
	}
}
