package executor

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type upstreamBodyReadLimit struct {
	responseBytes int64
	errorBytes    int64
}

const (
	defaultUpstreamResponseBodyLimit = 32 << 20 // 32 MiB
	defaultUpstreamErrorBodyLimit    = 1 << 20  // 1 MiB
)

func providerUpstreamBodyReadLimit(provider string) upstreamBodyReadLimit {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "codex-websocket", "claude", "gemini", "gemini-cli", "gemini-vertex", "antigravity":
		return upstreamBodyReadLimit{responseBytes: 64 << 20, errorBytes: 2 << 20}
	case "qwen", "kimi", "iflow", "opencode-go":
		return upstreamBodyReadLimit{responseBytes: 32 << 20, errorBytes: 2 << 20}
	default:
		return upstreamBodyReadLimit{responseBytes: defaultUpstreamResponseBodyLimit, errorBytes: defaultUpstreamErrorBodyLimit}
	}
}

func readUpstreamResponseBody(provider string, r io.Reader) ([]byte, error) {
	limit := providerUpstreamBodyReadLimit(provider).responseBytes
	data, truncated, err := readBodyAtMost(r, limit)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("%s upstream response body exceeds %s read limit", providerLabel(provider), formatByteLimit(limit))
	}
	return data, nil
}

func readUpstreamErrorBody(provider string, r io.Reader) []byte {
	limit := providerUpstreamBodyReadLimit(provider).errorBytes
	data, truncated, err := readBodyAtMost(r, limit)
	if err != nil {
		return []byte(fmt.Sprintf("failed to read upstream error body: %v", err))
	}
	if truncated {
		data = append(data, []byte(fmt.Sprintf("\n[cliproxy: upstream error body truncated at %s]", formatByteLimit(limit)))...)
	}
	return data
}

// readDecodedUpstreamErrorBody reads an upstream error body and decodes it per
// Content-Encoding before the bytes are turned into a client-facing message, a
// recorded response chunk, or a failure log entry.
//
// Anthropic compresses its error payloads (Content-Encoding: br) and the success
// path already decodes them via decodeResponseBody; the error path used the raw
// bytes, so a rate-limit response reached the client — and the logs — as binary
// noise instead of the message that explains it.
func readDecodedUpstreamErrorBody(provider string, header http.Header, r io.Reader) []byte {
	body := readUpstreamErrorBody(provider, r)
	if len(body) == 0 || header == nil {
		return body
	}
	encoding := strings.TrimSpace(header.Get("Content-Encoding"))
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return body
	}

	decoded, err := decodeResponseBody(io.NopCloser(bytes.NewReader(body)), encoding)
	if err != nil {
		return body
	}
	defer func() { _ = decoded.Close() }()

	limit := providerUpstreamBodyReadLimit(provider).errorBytes
	raw, truncated, err := readBodyAtMost(decoded, limit)
	if err != nil || len(raw) == 0 {
		return body
	}
	if truncated {
		raw = append(raw, []byte(fmt.Sprintf("\n[cliproxy: upstream error body truncated at %s]", formatByteLimit(limit)))...)
	}
	return raw
}

func readBodyAtMost(r io.Reader, limit int64) ([]byte, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	if limit <= 0 {
		data, err := io.ReadAll(r)
		return data, false, err
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return data, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

func providerLabel(provider string) string {
	if trimmed := strings.TrimSpace(provider); trimmed != "" {
		return trimmed
	}
	return "provider"
}

func formatByteLimit(limit int64) string {
	const mib = 1 << 20
	if limit > 0 && limit%mib == 0 {
		return fmt.Sprintf("%d MiB", limit/mib)
	}
	return fmt.Sprintf("%d bytes", limit)
}
