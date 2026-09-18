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

// dropDisabledModelIDs removes every model the operator switched off from an ID
// set. ConfiguredAvailability keeps disabled models so the management catalog can
// still render their toggle, which makes this the step every serving surface
// derived from it has to apply.
func dropDisabledModelIDs(ids map[string]struct{}, tenantID string) map[string]struct{} {
	if len(ids) == 0 {
		return ids
	}
	disabled := modelconfigsettings.DisabledModelIDsForTenant(tenantID)
	if len(disabled) == 0 {
		return ids
	}
	out := make(map[string]struct{}, len(ids))
	for id := range ids {
		if _, off := disabled[modelconfigsettings.NormalizeModelKey(id)]; off {
			continue
		}
		out[id] = struct{}{}
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

// defaultMappedOwnerRows returns the config rows the operator has switched on
// for the default owner scope, plus the key sets the caller needs to merge them.
// It lives here because the Enabled check is the additive half of the same
// toggle dropDisabledModels subtracts with.
func (s *Service) defaultMappedOwnerRows() ([]usage.ModelConfigRow, map[string]bool, map[string]bool, bool) {
	ownerKeys := s.defaultMappedOwnerKeys()
	if len(ownerKeys) == 0 {
		return nil, nil, nil, false
	}
	rows := modelconfigsettings.ListAllConfigsForTenant(s.tenantID)
	out := make([]usage.ModelConfigRow, 0, len(rows))
	configuredModelKeys := make(map[string]bool, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		if key := strings.ToLower(strings.TrimSpace(row.ModelID)); key != "" {
			configuredModelKeys[key] = true
		}
		if ownerKeys[normalizeModelOwnerKey(row.OwnedBy)] {
			out = append(out, row)
		}
	}
	return out, ownerKeys, configuredModelKeys, true
}
