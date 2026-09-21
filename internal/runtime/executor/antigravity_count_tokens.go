package executor

import (
	"context"
	"errors"
	"io"
	"net/http"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// CountTokens counts tokens for the given request using the Antigravity API.
func (e *AntigravityExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return cliproxyexecutor.Response{}, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}
	execCtx := e.newAntigravityExecutionContext(ctx, auth, req, opts, false)
	payload, err := e.buildAntigravityCountTokensPayload(execCtx)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := execCtx.HTTPClient(0)
	recorder := execCtx.Recorder()

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx, baseURL := range baseURLs {
		httpReq, requestBody, errReq := e.buildCountTokensRequest(execCtx.Context, auth, token, payload, opts.Alt, baseURL)
		if errReq != nil {
			return cliproxyexecutor.Response{}, errReq
		}
		recorder.RecordRequest(httpReq.URL.String(), httpReq.Method, httpReq.Header.Clone(), requestBody)

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			recorder.RecordResponseError(errDo)
			if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
				return cliproxyexecutor.Response{}, errDo
			}
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return cliproxyexecutor.Response{}, errDo
		}

		recorder.RecordResponseMetadata(httpResp.StatusCode, httpResp.Header.Clone())
		readBody := readUpstreamResponseBody
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			readBody = func(provider string, r io.Reader) ([]byte, error) {
				return readUpstreamErrorBody(provider, r), nil
			}
		}
		bodyBytes, errRead := readBody("antigravity", httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			recorder.RecordResponseError(errRead)
			return cliproxyexecutor.Response{}, errRead
		}
		recorder.AppendResponseChunk(bodyBytes)

		if httpResp.StatusCode >= http.StatusOK && httpResp.StatusCode < http.StatusMultipleChoices {
			count := gjson.GetBytes(bodyBytes, "totalTokens").Int()
			respCtx := context.WithValue(execCtx.Context, "alt", opts.Alt)
			translated := sdktranslator.TranslateTokenCount(respCtx, execCtx.Execution.TargetFormat, execCtx.SourceFormat, count, bodyBytes)
			return cliproxyexecutor.Response{Payload: []byte(translated), Headers: httpResp.Header.Clone()}, nil
		}

		// This branch now always returns below, so it no longer needs to
		// leave a lastStatus/lastBody/lastErr trail for the post-loop
		// switch — 429 is an account-level RESOURCE_EXHAUSTED signal, not a
		// host-specific hiccup: see antigravity_executor.go for the
		// production trace that motivated dropping the host fallback.
		//
		// The location gate is the opposite: it is a per-attempt draw
		// (antigravityShouldRetryHost), so the other daily host is worth a
		// try before the rejection reaches the client.
		if antigravityShouldRetryHost(httpResp.StatusCode, bodyBytes) && idx+1 < len(baseURLs) {
			log.Debugf("antigravity executor: retryable upstream status %d on base url %s, retrying with fallback base url: %s", httpResp.StatusCode, baseURL, baseURLs[idx+1])
			continue
		}
		sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
		if httpResp.StatusCode == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		return cliproxyexecutor.Response{}, sErr
	}

	switch {
	case lastStatus != 0:
		sErr := statusErr{code: lastStatus, msg: string(lastBody)}
		if lastStatus == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		return cliproxyexecutor.Response{}, sErr
	case lastErr != nil:
		return cliproxyexecutor.Response{}, lastErr
	default:
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
	}
}
