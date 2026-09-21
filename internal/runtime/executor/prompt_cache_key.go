package executor

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func applyProviderPromptCaching(payload, source []byte, auth *cliproxyauth.Auth, provider, model string, target sdktranslator.Format, opts cliproxyexecutor.Options) []byte {
	switch target {
	case sdktranslator.FormatClaude:
		if providerSupportsClaudeCacheControl(provider) && countCacheControls(payload) == 0 {
			return ensureCacheControl(payload)
		}
	case sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse:
		if providerSupportsSessionPromptCacheKey(provider) {
			return applySessionPromptCacheKey(payload, source, auth, model, opts)
		}
	}
	return payload
}

// applySessionPromptCacheKey derives a stable upstream cache key from the
// downstream session without exposing raw client IDs to the provider.
func applySessionPromptCacheKey(payload, source []byte, auth *cliproxyauth.Auth, model string, opts cliproxyexecutor.Options) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()) != "" {
		return payload
	}
	if explicit := strings.TrimSpace(gjson.GetBytes(source, "prompt_cache_key").String()); explicit != "" {
		updated, err := sjson.SetBytes(payload, "prompt_cache_key", explicit)
		if err == nil {
			return updated
		}
		return payload
	}
	seed := sessionPromptCacheSeed(opts, source)
	if seed == "" {
		seed = sessionPromptCacheSeed(opts, payload)
	}
	key := scopedPromptCacheKey(auth, model, seed)
	if key == "" {
		return payload
	}
	updated, err := sjson.SetBytes(payload, "prompt_cache_key", key)
	if err != nil {
		return payload
	}
	return updated
}

func sessionPromptCacheSeed(opts cliproxyexecutor.Options, payload []byte) string {
	if key := metadataText(opts.Metadata, cliproxyexecutor.SessionStickyMetadataKey); key != "" {
		return key
	}
	if key := metadataText(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); key != "" {
		return "execution:" + key
	}
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	for _, path := range []string{
		"session_id",
		"sessionId",
		"conversation_id",
		"conversationId",
		"metadata.session_id",
		"metadata.conversation_id",
		"metadata.user_id.session_id",
		"metadata.user_id",
	} {
		if key := strings.TrimSpace(gjson.GetBytes(payload, path).String()); key != "" {
			return path + ":" + key
		}
	}
	return ""
}

// codexSessionPromptCacheSeed extends the shared seed with the body-derived
// identity that selection uses when a client sends no session marker at all.
//
// The extra fallback is deliberately not in sessionPromptCacheSeed: providers
// that price cache reads separately key their own estimates off that seed, and
// widening it there changes which requests they count as the same conversation.
// Codex has no such estimate — its key exists only to steer upstream cache
// routing — so the looser identity is safe to use here and nowhere else.
func codexSessionPromptCacheSeed(opts cliproxyexecutor.Options, payload []byte) string {
	if seed := sessionPromptCacheSeed(opts, payload); seed != "" {
		return seed
	}
	return cliproxyauth.SessionKeyFromRequestBody(payload)
}

func metadataText(metadata map[string]any, key string) string {
	if metadata == nil || key == "" {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func providerSupportsClaudeCacheControl(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case openCodeGoProvider, "ollama-cloud":
		return true
	default:
		return false
	}
}

func providerSupportsSessionPromptCacheKey(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	// Command Code prices cache reads separately from fresh input, so a stable
	// per-session key is what lets a long conversation stay on the cheap side.
	case openCodeGoProvider, "cline", "ollama-cloud", commandCodeProvider:
		return true
	default:
		return false
	}
}
