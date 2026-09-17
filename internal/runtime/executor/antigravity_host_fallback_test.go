package executor

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// The sandbox host rejects some callers with a location gate while the daily and
// production hosts serve the identical request. That response is about the exit
// host, not about the request, so the executor has to move on to the next host
// instead of failing the client.
func TestAntigravityShouldRetryHost(t *testing.T) {
	locationBody := []byte(`{"error":{"code":400,"message":"User location is not supported for the API use.","status":"FAILED_PRECONDITION"}}`)
	cases := []struct {
		name   string
		status int
		body   []byte
		want   bool
	}{
		{"location rejection", http.StatusBadRequest, locationBody, true},
		{"location rejection lowercased", http.StatusBadRequest, []byte("user location is not supported for the api use"), true},
		{"empty body", http.StatusBadRequest, nil, false},
		{"other 400", http.StatusBadRequest, []byte(`{"error":{"message":"Request contains an invalid argument."}}`), false},
		{"invalid api key", http.StatusUnauthorized, locationBody, false},
		{"server error", http.StatusInternalServerError, locationBody, false},
		{"rate limited", http.StatusTooManyRequests, locationBody, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := antigravityShouldRetryHost(testCase.status, testCase.body); got != testCase.want {
				t.Fatalf("antigravityShouldRetryHost(%d, %q) = %v, want %v", testCase.status, testCase.body, got, testCase.want)
			}
		})
	}
}

// A host that only ever answers `429 quota` can never serve the caller that just
// got gated, and the 429 is read as an account-level signal that trips the
// credential cool-down for the requests that follow. Measured: production
// answered 6/6 requests with RESOURCE_EXHAUSTED for this free-tier account.
func TestAntigravityBaseURLFallbackOrderExcludesProduction(t *testing.T) {
	baseURLs := antigravityBaseURLFallbackOrder(nil)
	if len(baseURLs) != 2 {
		t.Fatalf("fallback hosts = %#v, want the two daily hosts", baseURLs)
	}
	if baseURLs[0] != antigravitySandboxBaseURLDaily {
		t.Fatalf("first host = %q, want the sandbox host (throttling order is deliberate)", baseURLs[0])
	}
	if baseURLs[1] != antigravityBaseURLDaily {
		t.Fatalf("second host = %q, want the daily host %q", baseURLs[1], antigravityBaseURLDaily)
	}
	for _, baseURL := range baseURLs {
		if baseURL == antigravityBaseURLProd {
			t.Fatalf("production host %q must stay out of the fallback order", antigravityBaseURLProd)
		}
	}
}

// The location gate has to share the retry branch with the capacity check: the
// gate is a per-attempt draw, so a second host *and* a second attempt are both
// worth taking before the rejection is handed to the client.
func TestAntigravityContentPathsRetryOnLocationGate(t *testing.T) {
	for _, path := range []string{"antigravity_executor.go", "antigravity_nonstream.go", "antigravity_count_tokens.go"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(source)
		hostCheck := strings.Index(body, "antigravityShouldRetryHost(httpResp.StatusCode, bodyBytes)")
		if hostCheck < 0 {
			t.Errorf("%s does not retry on the location gate", path)
			continue
		}
		statusErr := strings.Index(body[hostCheck:], "statusErr{code: httpResp.StatusCode")
		if statusErr < 0 {
			t.Errorf("%s: the gate check is not followed by the status error it must pre-empt", path)
			continue
		}
		between := body[hostCheck : hostCheck+statusErr]
		if !strings.Contains(between, "continue") {
			t.Errorf("%s checks the gate without continuing to the next base URL or attempt", path)
		}
		// Both predicates must be in the same condition, so the gate inherits the
		// existing next-host-then-next-attempt ladder.
		if path != "antigravity_count_tokens.go" {
			condition := body[strings.LastIndex(body[:hostCheck], "if "):hostCheck]
			if !strings.Contains(condition, "antigravityShouldRetryNoCapacity(httpResp.StatusCode, bodyBytes)") {
				t.Errorf("%s: the gate check does not share the retryable-status condition", path)
			}
		}
	}
}
