package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"streamnzb/pkg/core/httpx"
	"streamnzb/pkg/core/logger"
)

// shareFetchMaxBytes matches the browser-side cap in shareCodes.js: a share
// code is a few kilobytes, and a response past this is not a profile.
const shareFetchMaxBytes = 256 << 10

const shareFetchTimeout = 15 * time.Second

// shareFetchClient is the guarded client the share-code relay fetches with:
// every dial, redirects included, is checked against internal addresses, and
// redirects may not leave HTTPS. A var so a test can trust a local TLS stub.
var shareFetchClient = func(allowPrivate []*net.IPNet) *http.Client {
	return httpx.GuardedClient(shareFetchTimeout, allowPrivate)
}

// handleShareFetch relays a share-code URL the browser could not fetch
// itself — a host that sends no CORS headers, or a reverse proxy whose
// policy refuses cross-origin requests from the dashboard. The browser tries
// first; this is its fallback, so a source that works there never reaches
// the server. Admin only, https only, no credentials, size-capped, and the
// body is returned as text for the browser to decode and diff exactly as if
// it had fetched it: nothing here is parsed or applied.
func (s *Server) handleShareFetch(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can fetch share codes", http.MethodPost) {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "A URL is required.")
		return
	}
	target, err := url.Parse(strings.TrimSpace(body.URL))
	if err != nil || target.Scheme != "https" || target.Host == "" {
		writeJSONError(w, http.StatusBadRequest, "Only https:// URLs can be linked.")
		return
	}
	text, err := fetchShareText(r, target.String(), s.catalogSourceNetworks())
	if err != nil {
		logger.Warn("Share code fetch failed", "host", target.Host, "err", err)
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// fetchShareText fetches the URL and returns its body. Errors are worded for
// the dashboard and never carry the body: whatever an arbitrary URL returned
// does not belong in the UI.
func fetchShareText(r *http.Request, target string, allowPrivate []*net.IPNet) (string, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", errors.New("That is not a valid URL.")
	}
	req.Header.Set("Accept", "text/plain, */*")
	resp, err := shareFetchClient(allowPrivate).Do(req)
	if err != nil {
		return "", fmt.Errorf("StreamNZB could not fetch that URL either: %s", describeShareFetchError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("The URL answered HTTP %d.", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, shareFetchMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("StreamNZB could not read the response: %s", describeShareFetchError(err))
	}
	if len(data) > shareFetchMaxBytes {
		return "", errors.New("The URL returned far more data than a profile code.")
	}
	return string(data), nil
}

// describeShareFetchError strips the request line Go wraps a client error in
// ("Get \"https://…\": "), which repeats the URL the user just typed and
// buries the reason behind it.
func describeShareFetchError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "the host did not answer in time."
	}
	return err.Error()
}
