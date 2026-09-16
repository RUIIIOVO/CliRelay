package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	modelconfigsettings "github.com/router-for-me/CLIProxyAPI/v6/internal/management/settings/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"github.com/tidwall/gjson"
)

// Disabling a model in the catalog only ever stopped a config row from
// contributing a model of its own. Anything the static registry already served
// stayed listed and stayed callable, so "disabled" meant nothing for exactly
// the models an operator is most likely to switch off.

func initDisabledModelTestDB(t *testing.T) {
	t.Helper()
	usage.CloseDB()
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	if err := usage.InitDB(dbPath, config.RequestLogStorageConfig{}, time.UTC); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(usage.CloseDB)
}

func seedModelConfig(t *testing.T, modelID string, enabled bool) {
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

// runRestrictionMiddleware drives just the middleware, so the assertions are
// about the disabled-model decision and not about routing or upstream calls.
func runRestrictionMiddleware(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	server := &Server{cfg: &config.Config{}}
	engine := gin.New()
	engine.POST("/v1/chat/completions", server.modelRestrictionMiddleware(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"reached_upstream": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestDisabledModelRequestIsRejected(t *testing.T) {
	initDisabledModelTestDB(t)
	seedModelConfig(t, "claude-opus-4-6", false)

	rec := runRestrictionMiddleware(t, `{"model":"claude-opus-4-6"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
	if code := gjson.Get(rec.Body.String(), "error.code").String(); code != "model_not_found" {
		t.Errorf("error.code = %q, want model_not_found", code)
	}
	if strings.Contains(rec.Body.String(), "reached_upstream") {
		t.Error("request reached the upstream handler despite the model being disabled")
	}
}

func TestEnabledModelRequestPassesThrough(t *testing.T) {
	initDisabledModelTestDB(t)
	seedModelConfig(t, "claude-opus-4-6", false)
	seedModelConfig(t, "claude-opus-5", true)

	rec := runRestrictionMiddleware(t, `{"model":"claude-opus-5"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "reached_upstream") {
		t.Error("enabled model did not reach the upstream handler")
	}
}

func TestModelWithNoConfigRowPassesThrough(t *testing.T) {
	initDisabledModelTestDB(t)
	seedModelConfig(t, "claude-opus-4-6", false)

	// A model the operator never touched has no row at all; it must not be
	// swept up by the disabled check.
	rec := runRestrictionMiddleware(t, `{"model":"gpt-5"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestDisabledModelCheckIsCaseInsensitive(t *testing.T) {
	initDisabledModelTestDB(t)
	seedModelConfig(t, "claude-opus-4-6", false)

	rec := runRestrictionMiddleware(t, `{"model":"CLAUDE-Opus-4-6"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestPrefixedAliasOfDisabledModelIsRejected(t *testing.T) {
	initDisabledModelTestDB(t)
	seedModelConfig(t, "gpt-oss:20b", false)

	// Provider-prefixed aliases route to the bare ID, so they must not be a way
	// around the toggle.
	rec := runRestrictionMiddleware(t, `{"model":"ollama/gpt-oss:20b"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestRequestWithoutModelIsUntouched(t *testing.T) {
	initDisabledModelTestDB(t)
	seedModelConfig(t, "claude-opus-4-6", false)

	rec := runRestrictionMiddleware(t, `{}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestDropDisabledCatalogModelsFiltersListing(t *testing.T) {
	initDisabledModelTestDB(t)
	seedModelConfig(t, "claude-opus-4-6", false)
	seedModelConfig(t, "claude-opus-5", true)

	models := []map[string]interface{}{
		{"id": "claude-opus-4-6"},
		{"id": "claude-opus-5"},
		{"id": "gpt-5"},
	}

	got := dropDisabledCatalogModels("", models)

	var ids []string
	for _, model := range got {
		id, _ := model["id"].(string)
		ids = append(ids, id)
	}
	want := []string{"claude-opus-5", "gpt-5"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestDropDisabledCatalogModelsKeepsListWhenNothingDisabled(t *testing.T) {
	initDisabledModelTestDB(t)
	seedModelConfig(t, "claude-opus-5", true)

	models := []map[string]interface{}{{"id": "claude-opus-5"}, {"id": "gpt-5"}}
	if got := dropDisabledCatalogModels("", models); len(got) != 2 {
		encoded, _ := json.Marshal(got)
		t.Fatalf("dropDisabledCatalogModels() = %s, want both models", encoded)
	}
}
