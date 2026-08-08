package availnzb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
)

const (
	apiPath        = "/api/v1"
	apiKeyStateKey = "availnzb_api_key"
	DefaultAppName = "StreamNZB"
)

var ErrRegisterKeyIPAlreadyHasKey = errors.New("availnzb register: ip already has a key")

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	apiKeyMu    sync.RWMutex
	backbonesMu sync.RWMutex
	backbones   map[string]string
}

type ReportRequest struct {
	URL             string `json:"url"`
	ReleaseName     string `json:"release_name"`
	Size            int64  `json:"size"`
	CompressionType string `json:"compression_type,omitempty"`
	ProviderURL     string `json:"provider_url"`
	Status          bool   `json:"status"`
	ImdbID          string `json:"imdb_id,omitempty"`
	TmdbID          string `json:"tmdb_id,omitempty"`
	TvdbID          string `json:"tvdb_id,omitempty"`
	Season          int    `json:"season,omitempty"`
	Episode         int    `json:"episode,omitempty"`
}

type BackboneStatus struct {
	Text        string    `json:"text"`
	LastUpdated time.Time `json:"last_updated"`
	Healthy     bool      `json:"healthy"`
}

type ProviderStatus = BackboneStatus

type StatusResponse struct {
	URL          string                    `json:"url"`
	Available    bool                      `json:"available"`
	ReleaseName  string                    `json:"release_name,omitempty"`
	DownloadLink string                    `json:"download_link,omitempty"`
	Size         int64                     `json:"size,omitempty"`
	Summary      map[string]BackboneStatus `json:"summary"`
}

type MeResponse struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	IsActive               bool       `json:"is_active"`
	AppSource              string     `json:"app_source"`
	TrustLevel             string     `json:"trust_level"`
	TrustScore             float64    `json:"trust_score"`
	ReportCount            int        `json:"report_count"`
	PublicReportCount      int        `json:"public_report_count"`
	VerifiedReportCount    int        `json:"verified_report_count"`
	QuarantinedReportCount int        `json:"quarantined_report_count"`
	RolledBackReportCount  int        `json:"rolled_back_report_count"`
	LastReportAt           *time.Time `json:"last_report_at"`
	LastRollbackAt         *time.Time `json:"last_rollback_at"`
}

func (m *MeResponse) UnmarshalJSON(data []byte) error {
	type meResponseAlias MeResponse
	var raw struct {
		meResponseAlias
		ID             json.RawMessage `json:"id"`
		TrustLevel     json.RawMessage `json:"trust_level"`
		LastReportAt   json.RawMessage `json:"last_report_at"`
		LastRollbackAt json.RawMessage `json:"last_rollback_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := decodeFlexibleString(raw.ID)
	if err != nil {
		return fmt.Errorf("decode me response id: %w", err)
	}
	trustLevel, err := decodeFlexibleString(raw.TrustLevel)
	if err != nil {
		return fmt.Errorf("decode me response trust_level: %w", err)
	}
	lastReportAt, err := decodeFlexibleTime(raw.LastReportAt)
	if err != nil {
		return fmt.Errorf("decode me response last_report_at: %w", err)
	}
	lastRollbackAt, err := decodeFlexibleTime(raw.LastRollbackAt)
	if err != nil {
		return fmt.Errorf("decode me response last_rollback_at: %w", err)
	}
	*m = MeResponse(raw.meResponseAlias)
	m.ID = id
	m.TrustLevel = trustLevel
	m.LastReportAt = lastReportAt
	m.LastRollbackAt = lastRollbackAt
	return nil
}

type apiErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Reason  string `json:"reason"`
}

func decodeFlexibleString(data json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}

	var asString string
	if err := json.Unmarshal(trimmed, &asString); err == nil {
		return asString, nil
	}

	var asNumber json.Number
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&asNumber); err == nil {
		return asNumber.String(), nil
	}

	return "", fmt.Errorf("expected string or number, got %s", string(trimmed))
}

func decodeFlexibleTime(data json.RawMessage) (*time.Time, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, fmt.Errorf("expected string or null, got %s", string(trimmed))
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed, nil
		}
	}

	return nil, fmt.Errorf("unsupported time format %q", value)
}

type releaseItemJSON struct {
	URL             string                    `json:"url"`
	ReleaseName     string                    `json:"release_name,omitempty"`
	DownloadLink    string                    `json:"download_link,omitempty"`
	Size            int64                     `json:"size,omitempty"`
	CompressionType string                    `json:"compression_type,omitempty"`
	Indexer         string                    `json:"indexer"`
	Available       bool                      `json:"available"`
	Summary         map[string]BackboneStatus `json:"summary"`
}

type ReleaseWithStatus struct {
	*release.Release
	Available       bool
	CompressionType string
	Summary         map[string]BackboneStatus
}

type ReleasesResult struct {
	ImdbID   string
	Count    int
	Releases []*ReleaseWithStatus
}

type ReportMeta struct {
	ReleaseName     string
	Size            int64
	CompressionType string
	ImdbID          string
	TmdbID          string
	TvdbID          string
	Season          int
	Episode         int
}

func NewClient(baseURL, apiKey string) *Client {
	baseURL = strings.TrimSuffix(baseURL, "/")
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTP: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
	}
}

func (c *Client) GetAPIKey() string {
	c.apiKeyMu.RLock()
	defer c.apiKeyMu.RUnlock()
	return c.APIKey
}

func (c *Client) SetAPIKey(apiKey string) {
	c.apiKeyMu.Lock()
	c.APIKey = strings.TrimSpace(apiKey)
	c.apiKeyMu.Unlock()
}

func decodeAPIErrorMessage(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}

	var wrapped apiErrorResponse
	if err := json.Unmarshal(trimmed, &wrapped); err == nil {
		parts := make([]string, 0, 4)
		seen := make(map[string]struct{}, 4)
		for _, candidate := range []string{wrapped.Error, wrapped.Message, wrapped.Detail, wrapped.Reason} {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			key := strings.ToLower(candidate)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			parts = append(parts, candidate)
		}
		if len(parts) > 0 {
			return strings.Join(parts, ": ")
		}
	}

	return strings.TrimSpace(string(trimmed))
}

func (c *Client) ReportAvailability(releaseURL string, providerURL string, status bool, meta ReportMeta) error {
	if c.BaseURL == "" {
		logger.Debug("AvailNZB report skipped", "reason", "no base URL configured")
		return nil
	}
	apiKey := c.GetAPIKey()
	if apiKey == "" {
		logger.Debug("AvailNZB report skipped", "reason", "no API key configured")
		return nil
	}
	if meta.ReleaseName == "" {
		logger.Debug("AvailNZB report skipped", "reason", "no release_name in meta", "url", releaseURL)
		return nil
	}

	body := ReportRequest{
		URL:             releaseURL,
		ReleaseName:     meta.ReleaseName,
		Size:            meta.Size,
		CompressionType: meta.CompressionType,
		ProviderURL:     providerURL,
		Status:          status,
		TmdbID:          meta.TmdbID,
	}

	if meta.TvdbID != "" && (meta.Season > 0 || meta.Episode > 0) {
		body.TvdbID = meta.TvdbID
		body.Season = meta.Season
		body.Episode = meta.Episode
	} else if meta.ImdbID != "" {
		body.ImdbID = meta.ImdbID
	}
	if body.ImdbID == "" && body.TmdbID == "" && body.TvdbID == "" {
		logger.Debug("AvailNZB report skipped", "reason", "no imdb_id, tmdb_id or tvdb_id in meta", "url", releaseURL)
		return nil
	}

	logger.Debug("AvailNZB report", "url", releaseURL, "release_name", body.ReleaseName, "provider", providerURL, "status", status, "imdb_id", body.ImdbID, "tmdb_id", body.TmdbID, "tvdb_id", body.TvdbID, "season", body.Season, "episode", body.Episode)

	return c.doJSON(context.Background(), requestOptions{
		method:        "POST",
		path:          "/report",
		body:          body,
		authenticated: true,
		okStatuses:    []int{http.StatusOK, http.StatusAccepted},
		errPrefix:     "availnzb report",
		logAttrs:      []any{"url", releaseURL},
		classifyErr:   statusAuthErrClassifier("availnzb report", releaseURL),
	})
}

type backbonesResponse struct {
	Backbones         []string            `json:"backbones"`
	ProviderHostnames map[string][]string `json:"provider_hostnames"`
}

func (c *Client) RefreshBackbones() error {
	if c.BaseURL == "" {
		return nil
	}
	var wrapped backbonesResponse
	if err := c.doJSON(context.Background(), requestOptions{
		path:         "/backbones",
		optionalAuth: true,
		out:          &wrapped,
		errPrefix:    "availnzb backbones",
	}); err != nil {
		return err
	}
	m := make(map[string]string)
	for backbone, hostnames := range wrapped.ProviderHostnames {
		backbone = strings.TrimSpace(backbone)
		if backbone == "" {
			continue
		}
		for _, h := range hostnames {
			h = strings.ToLower(strings.TrimSpace(h))
			if h != "" {
				m[h] = backbone
			}
		}
	}
	c.backbonesMu.Lock()
	c.backbones = m
	c.backbonesMu.Unlock()
	logger.Debug("AvailNZB RefreshBackbones", "entries", len(m))
	return nil
}

func (c *Client) GetBackbones() (map[string]string, error) {
	c.backbonesMu.RLock()
	defer c.backbonesMu.RUnlock()
	if c.backbones == nil {
		return nil, nil
	}
	out := make(map[string]string, len(c.backbones))
	for k, v := range c.backbones {
		out[k] = v
	}
	return out, nil
}

func (c *Client) GetMe() (*MeResponse, error) {
	if c.BaseURL == "" {
		logger.Trace("AvailNZB GetMe skipped", "reason", "no base URL")
		return nil, nil
	}
	logger.Debug("AvailNZB GetMe")

	var me MeResponse
	if err := c.doJSON(context.Background(), requestOptions{
		path:         "/me",
		optionalAuth: true,
		out:          &me,
		errPrefix:    "availnzb me",
		classifyErr: func(status int, message string) error {
			if status == http.StatusForbidden && isAPIKeyTemporarilyAssignedMessage(message) {
				logger.Warn("AvailNZB GetMe blocked by temporary IP lease", "status", status, "reason", message)
				return fmt.Errorf("availnzb me: unexpected status code: %d: api key temporarily assigned to another ip", status)
			}
			return nil
		},
	}); err != nil {
		return nil, err
	}

	return &me, nil
}

func (c *Client) GetStatus(releaseURL string) (*StatusResponse, error) {
	if c.BaseURL == "" {
		logger.Trace("AvailNZB GetStatus skipped", "reason", "no base URL")
		return nil, nil
	}
	params := url.Values{}
	params.Set("url", releaseURL)

	logger.Debug("AvailNZB GetStatus", "url", releaseURL)

	var status StatusResponse
	err := c.doJSON(context.Background(), requestOptions{
		path:              "/status/url",
		query:             params.Encode(),
		optionalAuth:      true,
		out:               &status,
		errPrefix:         "availnzb status",
		missingIsNotFound: true,
		classifyErr:       statusAuthErrClassifier("availnzb status", releaseURL),
	})
	if errors.Is(err, errNotFound) {
		logger.Debug("AvailNZB GetStatus", "result", "not_found", "url", releaseURL)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	logger.Debug("AvailNZB GetStatus", "url", releaseURL, "available", status.Available, "backbones", len(status.Summary))
	return &status, nil
}

// statusAuthErrClassifier turns the two auth-shaped rejections the API can
// return into stable messages, so callers do not have to match server prose.
func statusAuthErrClassifier(prefix, releaseURL string) func(int, string) error {
	return func(status int, message string) error {
		if status == http.StatusUnauthorized && isAPIKeyMissingMessage(message) {
			logger.Error("AvailNZB request rejected", "op", prefix, "status", status, "url", releaseURL, "api_key_missing", true)
			return fmt.Errorf("%s: unexpected status code: %d: api key missing", prefix, status)
		}
		if status == http.StatusForbidden && isAPIKeyTemporarilyAssignedMessage(message) {
			logger.Warn("AvailNZB request blocked by temporary IP lease", "op", prefix, "status", status, "url", releaseURL, "reason", message)
			return fmt.Errorf("%s: unexpected status code: %d: api key temporarily assigned to another ip", prefix, status)
		}
		return nil
	}
}

type releasesResponseJSON struct {
	ImdbID   string            `json:"imdb_id,omitempty"`
	Count    int               `json:"count"`
	Releases []releaseItemJSON `json:"releases"`
}

func availReleasesLogArgs(imdbID, tmdbID, tvdbID string, season, episode int, extra ...any) []any {
	attrs := []any{
		"imdb_id", imdbID,
		"tmdb_id", tmdbID,
	}
	if strings.TrimSpace(tvdbID) != "" || season > 0 || episode > 0 {
		attrs = append(attrs,
			"tvdb_id", tvdbID,
			"season", season,
			"episode", episode,
		)
	}
	return append(attrs, extra...)
}

func (c *Client) GetReleases(imdbID string, tmdbID string, tvdbID string, season, episode int, indexers []string, providers []string) (*ReleasesResult, error) {
	if c.BaseURL == "" {
		logger.Trace("AvailNZB GetReleases skipped", "reason", "no base URL")
		return nil, nil
	}

	var path string
	switch {
	case tmdbID != "" && (season > 0 || episode > 0):
		path = fmt.Sprintf("/status/tmdb/%s/%d/%d", url.PathEscape(tmdbID), season, episode)
	case tvdbID != "" && (season > 0 || episode > 0):
		path = fmt.Sprintf("/status/tvdb/%s/%d/%d", url.PathEscape(tvdbID), season, episode)
	case tmdbID != "":
		path = "/status/tmdb/" + url.PathEscape(tmdbID)
	case imdbID != "":
		path = "/status/imdb/" + url.PathEscape(imdbID)
	default:
		return nil, fmt.Errorf("availnzb releases: need tmdb_id, imdb_id, or tvdb_id+season+episode")
	}
	params := url.Values{}
	if len(indexers) > 0 {
		params.Set("indexers", strings.Join(indexers, ","))
	}
	if len(providers) > 0 {
		params.Set("providers", strings.Join(providers, ","))
	}

	logAttrs := availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode)
	logger.Debug("AvailNZB GetReleases", append(append([]any{}, logAttrs...),
		"indexers", len(indexers), "providers", len(providers))...)

	var raw releasesResponseJSON
	if err := c.doJSON(context.Background(), requestOptions{
		path:         path,
		query:        params.Encode(),
		optionalAuth: true,
		out:          &raw,
		errPrefix:    "availnzb releases",
		logAttrs:     logAttrs,
		classifyErr:  statusAuthErrClassifier("availnzb releases", ""),
	}); err != nil {
		return nil, err
	}

	releases := make([]*ReleaseWithStatus, 0, len(raw.Releases))
	availableCount := 0
	for i := range raw.Releases {
		r := &raw.Releases[i]
		idx := r.Indexer
		if idx == "" {
			idx = "AvailNZB"
		}
		rel := &release.Release{
			Title:      r.ReleaseName,
			Link:       r.DownloadLink,
			DetailsURL: r.URL,
			Size:       r.Size,
			Indexer:    idx,
		}
		releases = append(releases, &ReleaseWithStatus{
			Release:         rel,
			Available:       r.Available,
			CompressionType: r.CompressionType,
			Summary:         r.Summary,
		})
		if r.Available {
			availableCount++
		}
	}
	logger.Debug("AvailNZB GetReleases finished", availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode,
		"raw_results", raw.Count,
		"available_results", availableCount,
	)...)
	return &ReleasesResult{ImdbID: raw.ImdbID, Count: raw.Count, Releases: releases}, nil
}

// latestSummaryUpdate returns the newest LastUpdated across all backbone
// reports, or the zero time when there are none.
func latestSummaryUpdate(summary map[string]BackboneStatus) time.Time {
	var newest time.Time
	for _, report := range summary {
		if report.LastUpdated.After(newest) {
			newest = report.LastUpdated
		}
	}
	return newest
}

// backboneSet maps provider hosts onto the set of backbones they sit behind.
func backboneSet(hostToBackbone map[string]string, hosts []string) map[string]bool {
	out := make(map[string]bool)
	for _, h := range hosts {
		if b := hostToBackbone[strings.ToLower(strings.TrimSpace(h))]; b != "" {
			out[b] = true
		}
	}
	return out
}

func (c *Client) OurBackbones(providerHosts []string) (map[string]bool, error) {
	m, err := c.GetBackbones()
	if err != nil || m == nil {
		return nil, err
	}
	return backboneSet(m, providerHosts), nil
}

func (c *Client) CheckPreDownload(releaseURL string, validProviderHosts []string) (available bool, lastUpdated time.Time, capableProvider string, err error) {
	logger.Debug("AvailNZB CheckPreDownload", "url", releaseURL, "our_providers", len(validProviderHosts))
	if c.BaseURL == "" || releaseURL == "" {
		logger.Trace("AvailNZB CheckPreDownload skipped", "reason", "no base URL or empty release URL")
		return false, time.Time{}, "", nil
	}

	status, err := c.GetStatus(releaseURL)
	if err != nil {
		logger.Debug("AvailNZB CheckPreDownload GetStatus failed", "url", releaseURL, "err", err)
		return false, time.Time{}, "", err
	}
	if status == nil {
		logger.Debug("AvailNZB CheckPreDownload", "result", "not_found", "url", releaseURL)
		return false, time.Time{}, "", nil
	}

	hostToBackbone, err := c.GetBackbones()
	if err != nil || len(hostToBackbone) == 0 {
		logger.Trace("AvailNZB CheckPreDownload", "result", "no_backbone_mapping", "err", err)
		if status.Available && len(status.Summary) > 0 {
			return true, latestSummaryUpdate(status.Summary), "", nil
		}
		return false, time.Time{}, "", nil
	}
	ourBackbones := backboneSet(hostToBackbone, validProviderHosts)
	if len(ourBackbones) == 0 {
		if status.Available && len(status.Summary) > 0 {
			return true, latestSummaryUpdate(status.Summary), "", nil
		}
		return false, time.Time{}, "", nil
	}

	for backbone, report := range status.Summary {
		if ourBackbones[backbone] && report.Healthy {
			if report.LastUpdated.After(lastUpdated) {
				lastUpdated = report.LastUpdated
			}
			available = true
			for _, h := range validProviderHosts {
				if hostToBackbone[strings.ToLower(strings.TrimSpace(h))] == backbone {
					capableProvider = h
					break
				}
			}
			if capableProvider == "" {
				capableProvider = backbone
			}
			break
		}
	}
	if status.Available && !available && len(status.Summary) > 0 {
		lastUpdated = latestSummaryUpdate(status.Summary)
		available = status.Available
	}

	logger.Debug("AvailNZB CheckPreDownload", "result", "found", "available", available, "capable_provider", capableProvider, "url", releaseURL)
	return available, lastUpdated, capableProvider, nil
}
