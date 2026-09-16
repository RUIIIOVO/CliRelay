package modelcatalog

import (
	"testing"

	modelconfigsettings "github.com/router-for-me/CLIProxyAPI/v6/internal/management/settings/modelconfig"
)

// The management catalog is built from the static registry, which never looked
// at model_configs.Enabled. Disabling a registry-backed model therefore left it
// listed in the management model picker; dropDisabledModels is the step that
// makes the toggle mean something there.

func seedEnablement(t *testing.T, modelID string, enabled bool) {
	t.Helper()
	if _, err := modelconfigsettings.UpsertConfig(modelconfigsettings.UpsertConfigInput{
		ModelID:     modelID,
		Scope:       "active",
		OwnedBy:     "anthropic",
		Enabled:     enabled,
		PricingMode: "call",
	}); err != nil {
		t.Fatalf("UpsertConfig(%q) error = %v", modelID, err)
	}
}

func modelIDsOf(models []map[string]any) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		id, _ := model["id"].(string)
		out = append(out, id)
	}
	return out
}

func TestDropDisabledModelsRemovesDisabledEntries(t *testing.T) {
	initModelCatalogTestDB(t)

	seedEnablement(t, "claude-opus-4-6", false)
	seedEnablement(t, "claude-sonnet-4-6", false)
	seedEnablement(t, "claude-opus-5", true)

	models := []map[string]any{
		{"id": "claude-opus-4-6"},
		{"id": "claude-opus-5"},
		{"id": "claude-sonnet-4-6"},
		{"id": "claude-fable-5-1"},
	}

	got := modelIDsOf(dropDisabledModels(models, ""))

	want := []string{"claude-opus-5", "claude-fable-5-1"}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}

func TestDropDisabledModelsKeepsEverythingWhenNothingDisabled(t *testing.T) {
	initModelCatalogTestDB(t)

	seedEnablement(t, "claude-opus-5", true)

	models := []map[string]any{{"id": "claude-opus-5"}, {"id": "claude-fable-5-1"}}
	if got := dropDisabledModels(models, ""); len(got) != 2 {
		t.Fatalf("dropDisabledModels() = %v, want both models", modelIDsOf(got))
	}
}

func TestDropDisabledModelsMatchesCaseInsensitively(t *testing.T) {
	initModelCatalogTestDB(t)

	seedEnablement(t, "claude-opus-4-6", false)

	models := []map[string]any{{"id": "Claude-Opus-4-6"}, {"id": "claude-opus-5"}}
	got := modelIDsOf(dropDisabledModels(models, ""))
	if len(got) != 1 || got[0] != "claude-opus-5" {
		t.Fatalf("ids = %v, want [claude-opus-5]", got)
	}
}

func TestDropDisabledModelsHandlesEmptyInput(t *testing.T) {
	initModelCatalogTestDB(t)

	seedEnablement(t, "claude-opus-4-6", false)

	if got := dropDisabledModels(nil, ""); len(got) != 0 {
		t.Fatalf("dropDisabledModels(nil) = %v, want empty", modelIDsOf(got))
	}
}
