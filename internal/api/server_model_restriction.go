package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/bodyutil"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/identity"
	modelconfigsettings "github.com/router-for-me/CLIProxyAPI/v6/internal/management/settings/modelconfig"
	internalrouting "github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func (s *Server) modelRestrictionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		allowedStr := ""
		if metadataVal, exists := c.Get("accessMetadata"); exists {
			if metadata, ok := metadataVal.(map[string]string); ok {
				allowedStr = strings.TrimSpace(metadata["allowed-models"])
			}
		}
		route := pathRouteContextFromGin(c)
		routeGroup := ""
		if route != nil {
			routeGroup = route.Group
		}
		allowedGroups := allowedChannelGroupsFromAccessMetadata(c)

		var ccSwitchAllowed map[string]struct{}
		if route != nil && route.CcSwitch != nil {
			ccSwitchAllowed = make(map[string]struct{})
			for _, mapping := range route.CcSwitch.ModelMappings {
				if t := strings.TrimSpace(mapping.TargetModel); t != "" {
					ccSwitchAllowed[t] = struct{}{}
				}
				if r := strings.TrimSpace(mapping.RequestModel); r != "" {
					ccSwitchAllowed[r] = struct{}{}
				}
			}
			if dm := strings.TrimSpace(route.CcSwitch.DefaultModel); dm != "" {
				ccSwitchAllowed[dm] = struct{}{}
			}
			if len(ccSwitchAllowed) == 0 {
				ccSwitchAllowed = nil
			}
		}

		tenantID := requestTenantID(c)
		hasScopedRestriction := s.hasScopedRoutingModelRestrictionForTenant(tenantID, routeGroup, allowedGroups)
		// A model switched off in the catalog is refused for everyone, so this
		// middleware has to inspect the body even when the key itself carries no
		// allowed-models restriction.
		disabledModels := modelconfigsettings.DisabledModelIDsForTenant(tenantID)
		if allowedStr == "" && !hasScopedRestriction && ccSwitchAllowed == nil && len(disabledModels) == 0 {
			c.Next()
			return
		}

		var allowedModels map[string]struct{}
		if allowedStr != "" {
			allowedModels = make(map[string]struct{})
			for _, m := range strings.Split(allowedStr, ",") {
				trimmed := strings.TrimSpace(m)
				if trimmed != "" {
					allowedModels[trimmed] = struct{}{}
				}
			}
		}
		if ccSwitchAllowed != nil {
			if len(allowedModels) == 0 {
				allowedModels = ccSwitchAllowed
			} else {
				for m := range allowedModels {
					if _, ok := ccSwitchAllowed[m]; !ok {
						delete(allowedModels, m)
					}
				}
			}
		}

		var bodyObj struct {
			Model string `json:"model"`
		}
		err := bodyutil.DecodeJSONRequestBody(c, bodyutil.ModelRequestBodyLimit(), &bodyObj)
		if err != nil {
			if bodyutil.IsTooLarge(err) {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
				return
			}
			if bodyutil.IsStorageUnavailable(err) {
				c.AbortWithStatusJSON(http.StatusInsufficientStorage, gin.H{"error": "request body temporary storage unavailable"})
				return
			}
			c.Next()
			return
		}
		if bodyObj.Model == "" {
			c.Next()
			return
		}
		requestedModel := strings.TrimSpace(bodyObj.Model)
		routingModel := mapCcSwitchRequestModelForRestriction(requestedModel, route)

		// Check the model actually routed to as well, so a CC Switch mapping cannot
		// reach a disabled target through an enabled request alias.
		if modelIsDisabled(requestedModel, disabledModels) || modelIsDisabled(routingModel, disabledModels) {
			abortModelDisabled(c, requestedModel)
			return
		}

		if len(allowedModels) > 0 && !modelInSet(requestedModel, allowedModels) && !modelInSet(routingModel, allowedModels) {
			abortModelNotAllowed(c, requestedModel)
			return
		}
		if !s.modelAllowedByScopedRoutingGroupsForTenant(tenantID, routingModel, routeGroup, allowedGroups) {
			abortModelNotAllowed(c, requestedModel)
			return
		}

		c.Next()
	}
}

func abortModelNotAllowed(c *gin.Context, model string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error": map[string]interface{}{
			"message": fmt.Sprintf("model '%s' is not allowed for this API key", model),
			"type":    "forbidden",
			"code":    "model_not_allowed",
		},
	})
}

// abortModelDisabled rejects a model the operator switched off in the catalog.
//
// This is not an authorization failure — the key may well be allowed to use it —
// so it reports the model as absent rather than forbidden, matching what the
// listing endpoints now say about it.
func abortModelDisabled(c *gin.Context, model string) {
	c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
		"error": map[string]interface{}{
			"message": fmt.Sprintf("model '%s' is disabled", model),
			"type":    "not_found",
			"code":    "model_not_found",
		},
	})
}

func modelIsDisabled(model string, disabled map[string]struct{}) bool {
	if len(disabled) == 0 {
		return false
	}
	key := modelconfigsettings.NormalizeModelKey(model)
	if key == "" {
		return false
	}
	if _, off := disabled[key]; off {
		return true
	}
	// Prefixed aliases ("ollama/gpt-oss:20b") route to the bare model ID.
	if idx := strings.Index(key, "/"); idx >= 0 {
		if _, off := disabled[strings.TrimSpace(key[idx+1:])]; off {
			return true
		}
	}
	return false
}

func modelInSet(model string, allowed map[string]struct{}) bool {
	model = strings.TrimSpace(model)
	if model == "" || len(allowed) == 0 {
		return false
	}
	_, ok := allowed[model]
	return ok
}

func mapCcSwitchRequestModelForRestriction(model string, route *internalrouting.PathRouteContext) string {
	model = strings.TrimSpace(model)
	if model == "" || route == nil || route.CcSwitch == nil {
		return model
	}
	for _, mapping := range route.CcSwitch.ModelMappings {
		if strings.EqualFold(strings.TrimSpace(mapping.RequestModel), model) {
			target := strings.TrimSpace(mapping.TargetModel)
			if target != "" {
				return target
			}
		}
	}
	return model
}

func (s *Server) hasScopedRoutingModelRestriction(routeGroup string, allowedGroups map[string]struct{}) bool {
	return s.hasScopedRoutingModelRestrictionForTenant(identity.SystemTenantID, routeGroup, allowedGroups)
}

func (s *Server) hasScopedRoutingModelRestrictionForTenant(tenantID, routeGroup string, allowedGroups map[string]struct{}) bool {
	return s.scopedRoutingAllowedModelsForTenant(tenantID, routeGroup, allowedGroups) != nil
}

func (s *Server) modelAllowedByScopedRoutingGroups(model string, routeGroup string, allowedGroups map[string]struct{}) bool {
	return s.modelAllowedByScopedRoutingGroupsForTenant(identity.SystemTenantID, model, routeGroup, allowedGroups)
}

func (s *Server) modelAllowedByScopedRoutingGroupsForTenant(tenantID, model, routeGroup string, allowedGroups map[string]struct{}) bool {
	allowedModels := s.scopedRoutingAllowedModelsForTenant(tenantID, routeGroup, allowedGroups)
	if allowedModels == nil {
		return true
	}
	return routeAllowedModelMatches(model, allowedModels)
}

func (s *Server) scopedRoutingAllowedModels(routeGroup string, allowedGroups map[string]struct{}) []string {
	return s.scopedRoutingAllowedModelsForTenant(identity.SystemTenantID, routeGroup, allowedGroups)
}

func (s *Server) scopedRoutingAllowedModelsForTenant(tenantID, routeGroup string, allowedGroups map[string]struct{}) []string {
	routing := s.routingConfigForTenant(tenantID)
	if routing == nil {
		return nil
	}
	scopedGroups := make(map[string]struct{})
	if routeGroup = internalrouting.NormalizeGroupName(routeGroup); routeGroup != "" {
		scopedGroups[routeGroup] = struct{}{}
	} else {
		for group := range allowedGroups {
			if normalized := internalrouting.NormalizeGroupName(group); normalized != "" {
				scopedGroups[normalized] = struct{}{}
			}
		}
	}
	if len(scopedGroups) == 0 {
		if routing.IncludeDefaultGroup {
			scopedGroups["default"] = struct{}{}
		}
	}
	if len(scopedGroups) == 0 {
		return nil
	}

	var allowedModels []string
	for _, group := range routing.ChannelGroups {
		groupName := internalrouting.NormalizeGroupName(group.Name)
		if _, ok := scopedGroups[groupName]; !ok {
			continue
		}
		if len(group.AllowedModels) == 0 {
			return nil
		}
		allowedModels = append(allowedModels, group.AllowedModels...)
	}
	if len(allowedModels) == 0 {
		return nil
	}
	return allowedModels
}

// routingConfigForTenant prefers the tenant's DB-backed routing config so
// channel-group allowed-models apply per tenant, not only on the system cfg.
func (s *Server) routingConfigForTenant(tenantID string) *config.RoutingConfig {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		tenantID = identity.SystemTenantID
	}
	if stored := usage.GetRoutingConfigForTenant(tenantID); stored != nil {
		return stored
	}
	if tenantID == identity.SystemTenantID && s != nil && s.cfg != nil {
		routing := s.cfg.Routing
		return &routing
	}
	return nil
}

func routeAllowedModelMatches(model string, allowedModels []string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, allowed := range allowedModels {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if strings.EqualFold(model, allowed) {
			return true
		}
		if idx := strings.Index(model, "/"); idx >= 0 && strings.EqualFold(strings.TrimSpace(model[idx+1:]), allowed) {
			return true
		}
	}
	return false
}
