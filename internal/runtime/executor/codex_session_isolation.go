package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// codexSyntheticSessionTTL bounds how long one derived session id is reused.
// Upstream drops a prompt prefix well before this, so the ceiling only caps how
// long an abandoned conversation occupies a map entry.
const codexSyntheticSessionTTL = time.Hour

func codexAccountScopedExplicitSessionID(auth *cliproxyauth.Auth, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	scope := sessionIsolationScope(auth)
	if scope == "" {
		return raw
	}
	return codexScopedIdentifier(scope, raw)
}

// codexSyntheticSessionID mints a stable session id for a client that sends no
// prompt_cache_key of its own.
//
// Upstream uses that key to route same-prefix requests to one prompt cache
// node; without it the routing is effectively arbitrary, so a conversation that
// resends a growing prefix every turn still misses most of the time. seed comes
// from the same body-derived identity that pins the conversation to this
// account, so every turn resolves to one id and the prefix lands on one node.
//
// The result is a fresh UUID rather than a hash of the seed: it is shaped like
// the identifier a real Codex client sends, and it keeps the seed — which is
// derived from request content — off the wire.
func codexSyntheticSessionID(auth *cliproxyauth.Auth, model, seed string) string {
	if strings.TrimSpace(seed) == "" {
		return ""
	}
	return getOrCreateCodexCacheID(codexPromptCacheMapKey(auth, model, seed), codexSyntheticSessionTTL, newCodexSessionID)
}

// newCodexSessionID mirrors the time-ordered session identifier a real Codex
// client generates.
func newCodexSessionID() string {
	if v, err := uuid.NewV7(); err == nil {
		return v.String()
	}
	return uuid.NewString()
}

// codexScopedIdentifier maps one client identifier into an account-scoped one
// while preserving the exact shape a real Codex client emits.
//
// The mapping is injective per scope: the same input always lands on the same
// output, and two different inputs never collide. That property is what keeps
// the client's own identity graph intact — a root turn where session_id equals
// thread_id still comes out with the two equal, and a sub-agent thread stays
// distinct from its root. Callers must therefore run every identifier of one
// request through this with the same scope, or the graph breaks apart.
//
// Shapes handled:
//   - UUID: version and variant nibbles are preserved, and for v7 so is the
//     48-bit timestamp, so the result is a well-formed, still time-ordered UUID
//     of the same version. Only the entropy is replaced.
//   - "<uuid>:<n>" (Codex window ids): the UUID part is mapped, the counter is
//     kept, because the counter tracks compaction rounds rather than identity.
//
// Anything else falls back to appending a scope-derived suffix. An identifier
// that is not a UUID did not come from an official Codex client, so there is no
// official shape left to preserve and account isolation is what matters.
func codexScopedIdentifier(scope, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || scope == "" {
		return raw
	}
	if uuidPart, suffix, ok := splitCodexWindowIdentifier(raw); ok {
		if mapped := scopeUUIDPreservingShape(scope, uuidPart); mapped != "" {
			return mapped + suffix
		}
	} else if mapped := scopeUUIDPreservingShape(scope, raw); mapped != "" {
		return mapped
	}
	sum := sha256.Sum256([]byte(scope + "\x00" + raw))
	return raw + "-" + hex.EncodeToString(sum[:8])
}

// splitCodexWindowIdentifier recognises the "<uuid>:<counter>" window id form.
func splitCodexWindowIdentifier(raw string) (string, string, bool) {
	idx := strings.LastIndex(raw, ":")
	if idx <= 0 || idx == len(raw)-1 {
		return "", "", false
	}
	for _, r := range raw[idx+1:] {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return raw[:idx], raw[idx:], true
}

// scopeUUIDPreservingShape rewrites a UUID's entropy from (scope, raw) and
// returns "" when raw is not a UUID, so callers can fall back.
func scopeUUIDPreservingShape(scope, raw string) string {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ""
	}
	original := [16]byte(parsed)
	sum := sha256.Sum256([]byte(scope + "\x00" + raw))

	var out [16]byte
	copy(out[:], sum[:16])
	// UUIDv7 carries a 48-bit big-endian millisecond timestamp in bytes 0..5.
	// Keeping it means the rewritten id sorts in the same order and sits in the
	// same time range as the client's, which a freshly hashed prefix would not.
	if parsed.Version() == 7 {
		copy(out[0:6], original[0:6])
	}
	// Restore the version nibble and the RFC 4122 variant bits so the result is
	// still a valid UUID of the very same version the client sent.
	out[6] = (out[6] & 0x0f) | (original[6] & 0xf0)
	out[8] = (out[8] & 0x3f) | (original[8] & 0xc0)
	return uuid.UUID(out).String()
}

func codexSessionIsolationScope(auth *cliproxyauth.Auth) string {
	return sessionIsolationScope(auth)
}

func sessionIsolationScope(auth *cliproxyauth.Auth) string {
	accountKey, authSubjectID := identityFingerprintAccount(auth)
	if strings.TrimSpace(accountKey) != "" {
		if strings.TrimSpace(authSubjectID) != "" {
			return "account:" + strings.TrimSpace(accountKey) + ":" + strings.TrimSpace(authSubjectID)
		}
		return "account:" + strings.TrimSpace(accountKey)
	}
	if auth == nil {
		return ""
	}
	if strings.TrimSpace(auth.ID) != "" {
		return "auth:" + strings.TrimSpace(auth.ID)
	}
	if idx := strings.TrimSpace(auth.EnsureIndex()); idx != "" {
		return "index:" + idx
	}
	if auth.Metadata != nil {
		for _, key := range []string{"account_id", "email", "access_token", "refresh_token"} {
			if value, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				sum := sha256.Sum256([]byte(key + "\x00" + strings.TrimSpace(value)))
				return "metadata:" + key + ":" + hex.EncodeToString(sum[:8])
			}
		}
	}
	return ""
}

func scopedPromptCacheKey(auth *cliproxyauth.Auth, model, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	model = strings.TrimSpace(model)
	scope := sessionIsolationScope(auth)
	sum := sha256.Sum256([]byte(scope + "\x00" + model + "\x00" + raw))
	return "clirelay-" + hex.EncodeToString(sum[:16])
}

func codexPromptCacheMapKey(auth *cliproxyauth.Auth, model, userID string) string {
	scope := sessionIsolationScope(auth)
	model = strings.TrimSpace(model)
	userID = strings.TrimSpace(userID)
	if scope == "" {
		return model + "-" + userID
	}
	return scope + ":" + model + "-" + userID
}
