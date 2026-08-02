package availnzb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type KeyStore interface {
	Get(key string, target interface{}) (bool, error)
	Set(key string, value interface{}) error
}

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

type KeyCreateRequest struct {
	Name string `json:"name"`
}

type KeyCreateResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

type RecoverKeyResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	IsActive  bool   `json:"is_active"`
	RotatedAt string `json:"rotated_at"`
}

func (r *RecoverKeyResponse) UnmarshalJSON(data []byte) error {
	type recoverKeyResponseAlias RecoverKeyResponse
	var raw struct {
		recoverKeyResponseAlias
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := decodeFlexibleString(raw.ID)
	if err != nil {
		return fmt.Errorf("decode recover key response id: %w", err)
	}
	*r = RecoverKeyResponse(raw.recoverKeyResponseAlias)
	r.ID = id
	return nil
}

type apiErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Reason  string `json:"reason"`
}

func (k *KeyCreateResponse) UnmarshalJSON(data []byte) error {
	type keyCreateResponseAlias KeyCreateResponse
	var raw struct {
		keyCreateResponseAlias
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := decodeFlexibleString(raw.ID)
	if err != nil {
		return fmt.Errorf("decode key create response id: %w", err)
	}
	*k = KeyCreateResponse(raw.keyCreateResponseAlias)
	k.ID = id
	return nil
}

type apiKeyState struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at,omitempty"`
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

func ResolveStartupAPIKey(store KeyStore, baseURL, explicitKey string) (string, error) {
	explicitKey = strings.TrimSpace(explicitKey)
	if explicitKey != "" {
		logger.Debug("AvailNZB key bootstrap using explicit API key")
		return explicitKey, nil
	}

	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		logger.Debug("AvailNZB key bootstrap skipped", "reason", "no base URL configured")
		return "", nil
	}
	if store == nil {
		return "", fmt.Errorf("availnzb key bootstrap: store is required")
	}

	storedKey, err := loadStoredAPIKey(store)
	if err != nil {
		return "", err
	}
	if storedKey != "" {
		logger.Debug("AvailNZB key bootstrap using stored API key")
	}
	return storedKey, nil
}

func ResolveAPIKey(store KeyStore, baseURL, explicitKey, appName string) (string, error) {
	if strings.TrimSpace(explicitKey) != "" {
		return strings.TrimSpace(explicitKey), nil
	}
	resolvedKey, err := ResolveStartupAPIKey(store, baseURL, explicitKey)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(baseURL) == "" {
		return resolvedKey, nil
	}
	if resolvedKey != "" {
		client := NewClient(baseURL, resolvedKey)
		if _, err := client.GetMe(); err == nil {
			return resolvedKey, nil
		}
		logger.Info("AvailNZB stored key validation failed, attempting recovery", "err", err)
		recovered, recoverErr := recoverAndPersistAPIKey(store, client)
		if recoverErr == nil && strings.TrimSpace(recovered) != "" {
			return recovered, nil
		}
		logger.Warn("AvailNZB key recovery failed, rotating key", "err", recoverErr)
	}

	return RecoverOrRegisterAndPersistAPIKey(store, baseURL, appName)
}

func RegisterAndPersistAPIKey(store KeyStore, baseURL, appName string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("availnzb register: no base URL configured")
	}
	if store == nil {
		return "", fmt.Errorf("availnzb key bootstrap: store is required")
	}
	if strings.TrimSpace(appName) == "" {
		appName = DefaultAppName
	}

	client := NewClient(baseURL, "")
	created, err := client.RegisterKey(appName)
	if err != nil {
		if errors.Is(err, ErrRegisterKeyIPAlreadyHasKey) {
			logger.Info("AvailNZB key registration refused (IP already has key), attempting recovery")
			return recoverAndPersistAPIKey(store, client)
		}
		return "", err
	}
	if created == nil || strings.TrimSpace(created.Token) == "" {
		return "", fmt.Errorf("availnzb register: empty token in response")
	}

	state := apiKeyState{
		ID:        strings.TrimSpace(created.ID),
		Name:      strings.TrimSpace(created.Name),
		Token:     strings.TrimSpace(created.Token),
		CreatedAt: strings.TrimSpace(created.CreatedAt),
	}
	if err := store.Set(apiKeyStateKey, state); err != nil {
		return "", fmt.Errorf("availnzb key bootstrap: failed to persist API key: %w", err)
	}

	logger.Info("Registered new AvailNZB API key", "name", state.Name, "id", state.ID)
	return state.Token, nil
}

func RecoverOrRegisterAndPersistAPIKey(store KeyStore, baseURL, appName string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("availnzb recover/register: no base URL configured")
	}
	if store == nil {
		return "", fmt.Errorf("availnzb key bootstrap: store is required")
	}
	if strings.TrimSpace(appName) == "" {
		appName = DefaultAppName
	}

	client := NewClient(baseURL, "")
	recovered, recoverErr := recoverAndPersistAPIKey(store, client)
	if recoverErr == nil && strings.TrimSpace(recovered) != "" {
		logger.Info("Recovered AvailNZB API key before rolling a new app key")
		return recovered, nil
	}
	logger.Info("AvailNZB key recovery failed, rolling new key", "err", recoverErr)

	return RegisterAndPersistAPIKey(store, baseURL, appName)
}

func recoverAndPersistAPIKey(store KeyStore, client *Client) (string, error) {
	recovered, err := client.RecoverKey()
	if err != nil {
		return "", fmt.Errorf("availnzb recover: %w", err)
	}
	if recovered == nil || strings.TrimSpace(recovered.Token) == "" {
		return "", fmt.Errorf("availnzb recover: empty token in response")
	}

	state := apiKeyState{
		ID:    strings.TrimSpace(recovered.ID),
		Name:  strings.TrimSpace(recovered.Name),
		Token: strings.TrimSpace(recovered.Token),
	}
	if err := store.Set(apiKeyStateKey, state); err != nil {
		return "", fmt.Errorf("availnzb key bootstrap: failed to persist recovered API key: %w", err)
	}

	logger.Info("Recovered existing AvailNZB API key", "name", state.Name, "id", state.ID)
	return state.Token, nil
}

func loadStoredAPIKey(store KeyStore) (string, error) {
	var state apiKeyState
	found, err := store.Get(apiKeyStateKey, &state)
	if err == nil {
		if found && strings.TrimSpace(state.Token) != "" {
			return strings.TrimSpace(state.Token), nil
		}
		return "", nil
	}

	var legacy string
	legacyFound, legacyErr := store.Get(apiKeyStateKey, &legacy)
	if legacyErr == nil {
		if legacyFound && strings.TrimSpace(legacy) != "" {
			return strings.TrimSpace(legacy), nil
		}
		return "", nil
	}

	return "", fmt.Errorf("availnzb key bootstrap: failed to read stored API key: %w", err)
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

func isRegisterKeyIPAlreadyHasKeyMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "already has a key for your ip") || strings.Contains(normalized, "already has a key for this ip") {
		return true
	}
	hasAlready := strings.Contains(normalized, "already") || strings.Contains(normalized, "existing") || strings.Contains(normalized, "exists")
	hasIP := strings.Contains(normalized, " ip") || strings.HasPrefix(normalized, "ip ") || strings.Contains(normalized, "ip address")
	hasKey := strings.Contains(normalized, "key")
	return hasAlready && hasIP && hasKey
}

func isAPIKeyMissingMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "x-api-key") && strings.Contains(normalized, "required")
}

func isAPIKeyTemporarilyAssignedMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "temporarily assigned to another ip")
}

func registerKeyIPAlreadyHasKeyErr(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return ErrRegisterKeyIPAlreadyHasKey
	}
	return fmt.Errorf("%w: %s", ErrRegisterKeyIPAlreadyHasKey, message)
}

func (c *Client) RegisterKey(name string) (*KeyCreateResponse, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("availnzb register: no base URL configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultAppName
	}

	logger.Debug("AvailNZB RegisterKey", "name", name)

	reqBody, err := json.Marshal(KeyCreateRequest{Name: name})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.BaseURL+apiPath+"/keys/roll_key", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Error("AvailNZB RegisterKey request failed", "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.Error("AvailNZB RegisterKey unexpected status and failed to read error body", "status", resp.StatusCode, "err", readErr)
			return nil, fmt.Errorf("availnzb register: unexpected status code: %d", resp.StatusCode)
		}
		message := decodeAPIErrorMessage(body)
		if isRegisterKeyIPAlreadyHasKeyMessage(message) {
			return nil, registerKeyIPAlreadyHasKeyErr(message)
		}
		if message != "" {
			logger.Error("AvailNZB RegisterKey unexpected status", "status", resp.StatusCode, "message", message)
			return nil, fmt.Errorf("availnzb register: unexpected status code: %d: %s", resp.StatusCode, message)
		}
		logger.Error("AvailNZB RegisterKey unexpected status", "status", resp.StatusCode)
		return nil, fmt.Errorf("availnzb register: unexpected status code: %d", resp.StatusCode)
	}

	var created KeyCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		logger.Error("AvailNZB RegisterKey decode failed", "err", err)
		return nil, err
	}

	return &created, nil
}

func (c *Client) RecoverKey() (*RecoverKeyResponse, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("availnzb recover: no base URL configured")
	}

	logger.Debug("AvailNZB RecoverKey")

	req, err := http.NewRequest("POST", c.BaseURL+apiPath+"/keys/recover", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(c.GetAPIKey()); token != "" {
		body, err := json.Marshal(map[string]string{"token": token})
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Error("AvailNZB RecoverKey request failed", "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("availnzb recover: unexpected status code: %d", resp.StatusCode)
		}
		message := decodeAPIErrorMessage(body)
		if message != "" {
			logger.Error("AvailNZB RecoverKey unexpected status", "status", resp.StatusCode, "message", message)
			return nil, fmt.Errorf("availnzb recover: unexpected status code: %d: %s", resp.StatusCode, message)
		}
		return nil, fmt.Errorf("availnzb recover: unexpected status code: %d", resp.StatusCode)
	}

	var recovered RecoverKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&recovered); err != nil {
		logger.Error("AvailNZB RecoverKey decode failed", "err", err)
		return nil, err
	}
	if id := strings.TrimSpace(recovered.ID); id == "" {
		return nil, fmt.Errorf("availnzb recover: empty id in response")
	}

	return &recovered, nil
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

	reqBody, err := json.Marshal(body)
	if err != nil {
		logger.Error("AvailNZB report marshal failed", "err", err)
		return err
	}

	req, err := http.NewRequest("POST", c.BaseURL+apiPath+"/report", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}

	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.Error("AvailNZB report unexpected status and failed to read error body", "status", resp.StatusCode, "url", releaseURL, "err", readErr)
			return fmt.Errorf("availnzb report: unexpected status code: %d", resp.StatusCode)
		}
		message := decodeAPIErrorMessage(body)
		if resp.StatusCode == http.StatusForbidden && isAPIKeyTemporarilyAssignedMessage(message) {
			logger.Warn("AvailNZB report blocked by temporary IP lease", "status", resp.StatusCode, "url", releaseURL, "reason", message)
			return fmt.Errorf("availnzb report: unexpected status code: %d: api key temporarily assigned to another ip", resp.StatusCode)
		}
		if message != "" {
			logger.Error("AvailNZB report unexpected status", "status", resp.StatusCode, "url", releaseURL, "message", message)
			return fmt.Errorf("availnzb report: unexpected status code: %d: %s", resp.StatusCode, message)
		}
		logger.Error("AvailNZB report unexpected status", "status", resp.StatusCode, "url", releaseURL)
		return fmt.Errorf("availnzb report: unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

type backbonesResponse struct {
	Backbones         []string            `json:"backbones"`
	ProviderHostnames map[string][]string `json:"provider_hostnames"`
}

func (c *Client) RefreshBackbones() error {
	if c.BaseURL == "" {
		return nil
	}
	apiKey := c.GetAPIKey()
	reqURL := c.BaseURL + apiPath + "/backbones"
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Error("AvailNZB RefreshBackbones request failed", "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Error("AvailNZB RefreshBackbones unexpected status", "status", resp.StatusCode)
		return fmt.Errorf("availnzb backbones: status %d", resp.StatusCode)
	}
	var wrapped backbonesResponse
	if err := json.NewDecoder(resp.Body).Decode(&wrapped); err != nil {
		logger.Error("AvailNZB RefreshBackbones decode failed", "err", err)
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
	apiKey := c.GetAPIKey()

	reqURL := c.BaseURL + apiPath + "/me"
	logger.Debug("AvailNZB GetMe")

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Error("AvailNZB GetMe request failed", "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Error("AvailNZB GetMe unexpected status", "status", resp.StatusCode)
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("availnzb me: unexpected status code: %d", resp.StatusCode)
		}
		message := decodeAPIErrorMessage(body)
		if resp.StatusCode == http.StatusForbidden && isAPIKeyTemporarilyAssignedMessage(message) {
			logger.Warn("AvailNZB GetMe blocked by temporary IP lease", "status", resp.StatusCode, "reason", message)
			return nil, fmt.Errorf("availnzb me: unexpected status code: %d: api key temporarily assigned to another ip", resp.StatusCode)
		}
		if message != "" {
			return nil, fmt.Errorf("availnzb me: unexpected status code: %d: %s", resp.StatusCode, message)
		}
		return nil, fmt.Errorf("availnzb me: unexpected status code: %d", resp.StatusCode)
	}

	var me MeResponse
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		logger.Error("AvailNZB GetMe decode failed", "err", err)
		return nil, err
	}

	return &me, nil
}

func (c *Client) GetStatus(releaseURL string) (*StatusResponse, error) {
	if c.BaseURL == "" {
		logger.Trace("AvailNZB GetStatus skipped", "reason", "no base URL")
		return nil, nil
	}
	apiKey := c.GetAPIKey()

	params := url.Values{}
	params.Set("url", releaseURL)
	reqURL := c.BaseURL + apiPath + "/status/url?" + params.Encode()

	logger.Debug("AvailNZB GetStatus", "url", releaseURL)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Error("AvailNZB GetStatus request failed", "err", err, "url", releaseURL)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		logger.Debug("AvailNZB GetStatus", "result", "not_found", "url", releaseURL)
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.Error("AvailNZB GetStatus unexpected status and failed to read error body", "status", resp.StatusCode, "url", releaseURL, "err", readErr)
			return nil, fmt.Errorf("availnzb status: unexpected status code: %d", resp.StatusCode)
		}
		message := decodeAPIErrorMessage(body)
		if resp.StatusCode == http.StatusUnauthorized && isAPIKeyMissingMessage(message) {
			logger.Error("AvailNZB GetStatus unexpected status", "status", resp.StatusCode, "url", releaseURL, "api_key_missing", true)
			return nil, fmt.Errorf("availnzb status: unexpected status code: %d: api key missing", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusForbidden && isAPIKeyTemporarilyAssignedMessage(message) {
			logger.Warn("AvailNZB GetStatus blocked by temporary IP lease", "status", resp.StatusCode, "url", releaseURL, "reason", message)
			return nil, fmt.Errorf("availnzb status: unexpected status code: %d: api key temporarily assigned to another ip", resp.StatusCode)
		}
		if message != "" {
			logger.Error("AvailNZB GetStatus unexpected status", "status", resp.StatusCode, "url", releaseURL, "message", message)
			return nil, fmt.Errorf("availnzb status: unexpected status code: %d: %s", resp.StatusCode, message)
		}
		logger.Error("AvailNZB GetStatus unexpected status", "status", resp.StatusCode, "url", releaseURL)
		return nil, fmt.Errorf("availnzb status: unexpected status code: %d", resp.StatusCode)
	}

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		logger.Error("AvailNZB GetStatus decode failed", "err", err)
		return nil, err
	}

	logger.Debug("AvailNZB GetStatus", "url", releaseURL, "available", status.Available, "backbones", len(status.Summary))
	return &status, nil
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
	apiKey := c.GetAPIKey()

	var path string
	if tmdbID != "" && (season > 0 || episode > 0) {
		path = fmt.Sprintf("%s/status/tmdb/%s/%d/%d", apiPath, url.PathEscape(tmdbID), season, episode)
	} else if tvdbID != "" && (season > 0 || episode > 0) {
		path = fmt.Sprintf("%s/status/tvdb/%s/%d/%d", apiPath, url.PathEscape(tvdbID), season, episode)
	} else if tmdbID != "" {
		path = apiPath + "/status/tmdb/" + url.PathEscape(tmdbID)
	} else if imdbID != "" {
		path = apiPath + "/status/imdb/" + url.PathEscape(imdbID)
	} else {
		return nil, fmt.Errorf("availnzb releases: need tmdb_id, imdb_id, or tvdb_id+season+episode")
	}
	params := url.Values{}
	if len(indexers) > 0 {
		params.Set("indexers", strings.Join(indexers, ","))
	}
	if len(providers) > 0 {
		params.Set("providers", strings.Join(providers, ","))
	}
	reqURL := c.BaseURL + path
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	logger.Debug("AvailNZB GetReleases", availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode,
		"indexers", len(indexers),
		"providers", len(providers),
	)...)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Error("AvailNZB GetReleases request failed", availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode,
			"err", err,
		)...)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.Error("AvailNZB GetReleases unexpected status and failed to read error body", availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode,
				"status", resp.StatusCode,
				"err", readErr,
			)...)
			return nil, fmt.Errorf("availnzb releases: unexpected status code: %d", resp.StatusCode)
		}
		message := decodeAPIErrorMessage(body)
		if resp.StatusCode == http.StatusUnauthorized && isAPIKeyMissingMessage(message) {
			logger.Error("AvailNZB GetReleases unexpected status", availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode,
				"status", resp.StatusCode,
				"api_key_missing", true,
			)...)
			return nil, fmt.Errorf("availnzb releases: unexpected status code: %d: api key missing", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusForbidden && isAPIKeyTemporarilyAssignedMessage(message) {
			logger.Warn("AvailNZB GetReleases blocked by temporary IP lease", availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode,
				"status", resp.StatusCode,
				"reason", message,
			)...)
			return nil, fmt.Errorf("availnzb releases: unexpected status code: %d: api key temporarily assigned to another ip", resp.StatusCode)
		}
		if message != "" {
			logger.Error("AvailNZB GetReleases unexpected status", availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode,
				"status", resp.StatusCode,
				"message", message,
			)...)
			return nil, fmt.Errorf("availnzb releases: unexpected status code: %d: %s", resp.StatusCode, message)
		}
		logger.Error("AvailNZB GetReleases unexpected status", availReleasesLogArgs(imdbID, tmdbID, tvdbID, season, episode,
			"status", resp.StatusCode,
		)...)
		return nil, fmt.Errorf("availnzb releases: unexpected status code: %d", resp.StatusCode)
	}

	var raw releasesResponseJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		logger.Error("AvailNZB GetReleases decode failed", "err", err)
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

func (c *Client) OurBackbones(providerHosts []string) (map[string]bool, error) {
	m, err := c.GetBackbones()
	if err != nil || m == nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, h := range providerHosts {
		if b := m[strings.ToLower(strings.TrimSpace(h))]; b != "" {
			out[b] = true
		}
	}
	return out, nil
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
			for _, report := range status.Summary {
				if report.LastUpdated.After(lastUpdated) {
					lastUpdated = report.LastUpdated
				}
			}
			return true, lastUpdated, "", nil
		}
		return false, time.Time{}, "", nil
	}
	ourBackbones := make(map[string]bool)
	for _, h := range validProviderHosts {
		if b := hostToBackbone[strings.ToLower(strings.TrimSpace(h))]; b != "" {
			ourBackbones[b] = true
		}
	}
	if len(ourBackbones) == 0 {
		if status.Available && len(status.Summary) > 0 {
			for _, report := range status.Summary {
				if report.LastUpdated.After(lastUpdated) {
					lastUpdated = report.LastUpdated
				}
			}
			return true, lastUpdated, "", nil
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
		for _, report := range status.Summary {
			if report.LastUpdated.After(lastUpdated) {
				lastUpdated = report.LastUpdated
			}
		}
		available = status.Available
	}

	logger.Debug("AvailNZB CheckPreDownload", "result", "found", "available", available, "capable_provider", capableProvider, "url", releaseURL)
	return available, lastUpdated, capableProvider, nil
}
