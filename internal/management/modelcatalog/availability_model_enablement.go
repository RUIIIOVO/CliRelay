package modelcatalog

// Model enablement scoping: which catalog models an operator's enable/disable
// toggles keep in play, both additively (config rows that contribute a model)
// and subtractively (models removed from whatever is about to be served).

import (
	"strings"

	modelconfigsettings "github.com/router-for-me/CLIProxyAPI/v6/internal/management/settings/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// dropDisabledModels removes every model the operator switched off in the model
// catalog.
//
// The additive Enabled checks elsewhere in this file only decide whether a
// model_configs row contributes a model of its own. Models the static registry
// already provides never pass through those checks, so without this step
// disabling one of them changed nothing at all.
func dropDisabledModels(models []map[string]any, tenantID string) []map[string]any {
	if len(models) == 0 {
		return models
	}
	disabled := modelconfigsettings.DisabledModelIDsForTenant(tenantID)
	if len(disabled) == 0 {
		return models
	}
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id, _ := model["id"].(string)
		if _, off := disabled[modelconfigsettings.NormalizeModelKey(id)]; off {
			continue
		}
		out = append(out, model)
	}
	return out
}

func mappedOwnerRowModelKeys(rows []usage.ModelConfigRow, ownerKeys map[string]bool) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := strings.ToLower(strings.TrimSpace(row.ModelID))
		if key == "" || !row.Enabled || !ownerKeys[normalizeModelOwnerKey(row.OwnedBy)] {
			continue
		}
		out[key] = true
	}
	return out
}
