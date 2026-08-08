package availnzb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"streamnzb/pkg/core/logger"
)

// errNotFound signals a 404 for endpoints where "no record yet" is a normal
// answer rather than a failure.
var errNotFound = errors.New("availnzb: not found")

// requestOptions describes one AvailNZB API call. Every endpoint goes through
// doJSON so the read-body / decode-error / status-ladder sequence exists once
// instead of being repeated per method.
type requestOptions struct {
	// method defaults to GET.
	method string
	// path is appended to BaseURL+apiPath, e.g. "/keys/recover".
	path string
	// query is appended verbatim (already encoded), without a leading "?".
	query string
	// body, when non-nil, is JSON-encoded and sent with a JSON content type.
	body interface{}
	// authenticated sends the configured API key; requests without a key fail
	// early rather than being rejected by the server.
	authenticated bool
	// optionalAuth sends the API key when one is configured but does not
	// require it (endpoints that answer differently for anonymous callers).
	optionalAuth bool
	// okStatuses are the status codes treated as success. Defaults to 200.
	okStatuses []int
	// out, when non-nil, receives the decoded JSON response body.
	out interface{}
	// errPrefix labels errors, e.g. "availnzb register".
	errPrefix string
	// logAttrs are appended to every log record this request emits, so callers
	// keep the request context (release url, ids) they logged before.
	logAttrs []any
	// missingIsNotFound maps a 404 onto errNotFound so callers can report
	// "no record" without treating it as a failure.
	missingIsNotFound bool
	// classifyErr optionally maps a non-OK response onto a typed error. It is
	// consulted before the generic status error is built; returning nil falls
	// through to the generic error.
	classifyErr func(status int, message string) error
}

func (c *Client) doJSON(ctx context.Context, opts requestOptions) error {
	if c.BaseURL == "" {
		return fmt.Errorf("%s: no base URL configured", opts.errPrefix)
	}
	method := opts.method
	if method == "" {
		method = http.MethodGet
	}

	var apiKey string
	if opts.authenticated || opts.optionalAuth {
		apiKey = strings.TrimSpace(c.GetAPIKey())
		if apiKey == "" && opts.authenticated {
			return fmt.Errorf("%s: no API key configured", opts.errPrefix)
		}
	}

	url := c.BaseURL + apiPath + opts.path
	if opts.query != "" {
		url += "?" + opts.query
	}

	var bodyReader io.Reader
	if opts.body != nil {
		encoded, err := json.Marshal(opts.body)
		if err != nil {
			return fmt.Errorf("%s: encode request: %w", opts.errPrefix, err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	if opts.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Error("AvailNZB request failed", append([]any{"op", opts.errPrefix, "err", err}, opts.logAttrs...)...)
		return err
	}
	defer resp.Body.Close()

	if opts.missingIsNotFound && resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}

	if !statusAccepted(resp.StatusCode, opts.okStatuses) {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.Error("AvailNZB unexpected status and failed to read error body",
				append([]any{"op", opts.errPrefix, "status", resp.StatusCode, "err", readErr}, opts.logAttrs...)...)
			return fmt.Errorf("%s: unexpected status code: %d", opts.errPrefix, resp.StatusCode)
		}
		message := decodeAPIErrorMessage(body)
		if opts.classifyErr != nil {
			if typed := opts.classifyErr(resp.StatusCode, message); typed != nil {
				return typed
			}
		}
		if message != "" {
			logger.Error("AvailNZB unexpected status", append([]any{"op", opts.errPrefix, "status", resp.StatusCode, "message", message}, opts.logAttrs...)...)
			return fmt.Errorf("%s: unexpected status code: %d: %s", opts.errPrefix, resp.StatusCode, message)
		}
		logger.Error("AvailNZB unexpected status", append([]any{"op", opts.errPrefix, "status", resp.StatusCode}, opts.logAttrs...)...)
		return fmt.Errorf("%s: unexpected status code: %d", opts.errPrefix, resp.StatusCode)
	}

	if opts.out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(opts.out); err != nil {
		logger.Error("AvailNZB decode failed", append([]any{"op", opts.errPrefix, "err", err}, opts.logAttrs...)...)
		return err
	}
	return nil
}

func statusAccepted(status int, accepted []int) bool {
	if len(accepted) == 0 {
		return status == http.StatusOK
	}
	for _, code := range accepted {
		if status == code {
			return true
		}
	}
	return false
}
