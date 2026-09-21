package executor

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

const sampleRateLimitErrorJSON = `{"type":"error","error":{"type":"rate_limit_error","message":"7 day limit reached"}}`

func decodeErrorBodyWithHeader(t *testing.T, body []byte, encoding string) string {
	t.Helper()
	header := http.Header{}
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
	}
	return string(readDecodedUpstreamErrorBody("claude", header, bytes.NewReader(body)))
}

// Anthropic answers 429 with a brotli-compressed body. Reading it raw made the
// client-facing error a blob of binary noise, which is exactly the case an
// operator needs to be able to read.
func TestReadDecodedUpstreamErrorBodyDecodesBrotli(t *testing.T) {
	var buf bytes.Buffer
	writer := brotli.NewWriter(&buf)
	if _, err := writer.Write([]byte(sampleRateLimitErrorJSON)); err != nil {
		t.Fatalf("brotli write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("brotli close: %v", err)
	}

	got := decodeErrorBodyWithHeader(t, buf.Bytes(), "br")
	if got != sampleRateLimitErrorJSON {
		t.Fatalf("decoded body = %q, want %q", got, sampleRateLimitErrorJSON)
	}
}

func TestReadDecodedUpstreamErrorBodyDecodesGzip(t *testing.T) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write([]byte(sampleRateLimitErrorJSON)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	if got := decodeErrorBodyWithHeader(t, buf.Bytes(), "gzip"); got != sampleRateLimitErrorJSON {
		t.Fatalf("decoded body = %q, want %q", got, sampleRateLimitErrorJSON)
	}
}

func TestReadDecodedUpstreamErrorBodyPassesThroughUnencoded(t *testing.T) {
	for _, encoding := range []string{"", "identity"} {
		if got := decodeErrorBodyWithHeader(t, []byte(sampleRateLimitErrorJSON), encoding); got != sampleRateLimitErrorJSON {
			t.Fatalf("Content-Encoding %q: body = %q, want the raw body", encoding, got)
		}
	}
}

// A body that does not actually match its declared encoding must still be
// surfaced: the error text is the only diagnostic the caller gets.
func TestReadDecodedUpstreamErrorBodyKeepsUndecodableBody(t *testing.T) {
	raw := []byte("not brotli at all")
	got := decodeErrorBodyWithHeader(t, raw, "br")
	if !strings.Contains(got, "not brotli") {
		t.Fatalf("body = %q, want the raw body preserved", got)
	}
}
