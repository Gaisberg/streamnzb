// Package mdblist reports playback to a user's MDBList account.
//
// An account belongs to a stream, not to the server: MDBList history is one
// person's, so a household where everyone has their own stream needs a link
// each. Registry hands out one Client per stream, each with its own tokens.
//
// The account is linked through MDBList's OAuth device-code flow, the same
// shape as Simkl's PIN flow and for the same reason: StreamNZB is self-hosted,
// so its URL is whatever the operator runs it on and there is no redirect URI
// that could be registered in advance. A device app needs none.
//
// One thing differs from Simkl in a way that matters. Simkl's access token
// never expires; MDBList's lasts thirty days and comes with a refresh token,
// so the token has to be renewed or scrobbling would quietly stop working a
// month after the account was linked. Renewal is serialized on refreshMu:
// MDBList may rotate the refresh token, so two concurrent refreshes would
// leave one of them holding a token that has already been spent.
package mdblist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/scrobble"
)

const (
	// stateKey holds every stream's tokens; legacyStateKey is the single
	// server-wide token written before accounts were per stream.
	stateKey       = "mdblist_tokens"
	legacyStateKey = "mdblist_token"

	// refreshMargin renews a token this long before it actually expires, so a
	// scrobble never races the expiry it was about to cross.
	refreshMargin = time.Hour

	// deviceScope is the only scope MDBList issues; it covers playback state.
	deviceScope = "write"
)

type tokenState struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresAt is when the access token stops being accepted, absolute so it
	// survives a restart (a stored "expires in 30 days" would not).
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at"`
	// Fingerprint pins the token to the client id it was authorized for.
	Fingerprint string `json:"fingerprint,omitempty"`
	// UserName is the display name captured when the account was linked, so
	// the settings page can say who is connected without an API call.
	UserName string `json:"user_name,omitempty"`
}

// expiringSoon reports whether the access token is gone or close enough to
// gone that it should be renewed before being used again.
func (t tokenState) expiringSoon() bool {
	if t.AccessToken == "" {
		return true
	}
	if t.ExpiresAt == "" {
		// A token with no recorded expiry is treated as good: MDBList always
		// sends expires_in, so this only happens for state written by hand.
		return false
	}
	at, err := time.Parse(time.RFC3339, t.ExpiresAt)
	if err != nil {
		return true
	}
	return time.Now().Add(refreshMargin).After(at)
}

// pendingDevice is one started device authorization. The device code stays on
// the server — it is the bearer of the pending grant, and the settings page
// has no need to hold it to poll.
type pendingDevice struct {
	deviceCode string
	expiresAt  time.Time
}

type Client struct {
	clientID string
	stream   string
	store    *scrobble.Store[tokenState]
	client   *http.Client
	BaseURL  string

	// tokenMu guards the persisted-token mirror. Never held across a request:
	// a rejected token is cleared from inside the request path.
	tokenMu     sync.Mutex
	tokenLoaded bool
	token       tokenState

	// refreshMu serializes renewal so a rotated refresh token is spent once.
	refreshMu sync.Mutex

	// deviceMu guards the in-flight authorization, replaced whenever the user
	// starts a new one.
	deviceMu sync.Mutex
	pending  *pendingDevice
}

// NewClient builds the client for one stream's account. Prefer a Registry, so
// one stream's renewed token is not overwritten by a second instance holding
// the token it replaced.
func NewClient(clientID, dataDir, stream string) *Client {
	return newClient(clientID, stream, scrobble.NewStore[tokenState](dataDir, stateKey))
}

func newClient(clientID, stream string, store *scrobble.Store[tokenState]) *Client {
	baseURL := "https://api.mdblist.com"
	if envURL := os.Getenv("STREAMNZB_MDBLIST_BASE_URL"); envURL != "" {
		baseURL = envURL
	}
	return &Client{
		clientID: strings.TrimSpace(clientID),
		stream:   strings.TrimSpace(stream),
		store:    store,
		client:   &http.Client{Timeout: 15 * time.Second},
		BaseURL:  baseURL,
	}
}

// ClientID reports the id the client was built with, so a reload can keep the
// instance — and its token mirror — when the effective id did not change.
func (c *Client) ClientID() string {
	if c == nil {
		return ""
	}
	return c.clientID
}

// Enabled reports whether a client id is available at all. Without one the
// device flow cannot start and the MDBList card asks for one instead.
func (c *Client) Enabled() bool { return c != nil && c.clientID != "" }

// Connected reports whether a linked account's token is on file.
func (c *Client) Connected() bool { return c.snapshot().AccessToken != "" }

// UserName is the linked account's display name, or "" when disconnected.
func (c *Client) UserName() string { return c.snapshot().UserName }

// Name labels the target in scrobble logs.
func (c *Client) Name() string { return "MDBList" }

// snapshot returns the persisted token state, loading it from the state store
// on first use. A token minted under a different client id is ignored — the
// account has to be re-linked.
func (c *Client) snapshot() tokenState {
	if c == nil || c.clientID == "" || c.stream == "" {
		return tokenState{}
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.tokenLoaded {
		return c.token
	}
	c.tokenLoaded = true
	if stored, ok := c.store.Get(c.stream); ok && stored.AccessToken != "" {
		if stored.Fingerprint == scrobble.CredentialFingerprint(c.clientID) {
			c.token = stored
		} else {
			logger.Debug("MDBList token was authorized for a different client id; account needs re-linking",
				"stream", c.stream)
		}
	}
	return c.token
}

func (c *Client) persist(state tokenState) {
	state.Fingerprint = scrobble.CredentialFingerprint(c.clientID)
	c.store.Set(c.stream, state)
	c.tokenMu.Lock()
	c.tokenLoaded = true
	c.token = state
	c.tokenMu.Unlock()
}

// Disconnect unlinks the account.
func (c *Client) Disconnect() {
	if c == nil {
		return
	}
	c.dropToken()
	c.deviceMu.Lock()
	c.pending = nil
	c.deviceMu.Unlock()
}

func (c *Client) dropToken() {
	c.store.Delete(c.stream)
	c.tokenMu.Lock()
	c.tokenLoaded = true
	c.token = tokenState{}
	c.tokenMu.Unlock()
}

// DeviceStart is one started device authorization: what to show the user, and
// the polling contract for CheckDevice.
type DeviceStart struct {
	UserCode string `json:"user_code"`
	// VerificationURI is where the code is entered; VerificationURIComplete is
	// the same page with the code already filled in, so a user who can follow
	// a link only has to confirm.
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartDevice begins the device-code flow and returns the code to display.
func (c *Client) StartDevice(ctx context.Context) (*DeviceStart, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("no MDBList client id is configured")
	}
	var out struct {
		DeviceStart
		DeviceCode string `json:"device_code"`
	}
	body, status, err := c.postForm(ctx, "/oauth/device-authorization/", url.Values{
		"client_id": {c.clientID},
		"scope":     {deviceScope},
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || json.Unmarshal(body, &out) != nil || out.DeviceCode == "" {
		return nil, fmt.Errorf("mdblist device authorization failed: %s", oauthErrorDetail(body, status))
	}
	if out.VerificationURI == "" {
		out.VerificationURI = "https://mdblist.com/oauth/device/"
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 300
	}
	c.deviceMu.Lock()
	c.pending = &pendingDevice{
		deviceCode: out.DeviceCode,
		expiresAt:  time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}
	c.deviceMu.Unlock()
	return &out.DeviceStart, nil
}

// CheckDevice polls the authorization started by StartDevice. It returns true
// once the user has approved — at which point the token is already persisted
// and the account is linked — and false while approval is still pending.
func (c *Client) CheckDevice(ctx context.Context) (bool, error) {
	c.deviceMu.Lock()
	pending := c.pending
	c.deviceMu.Unlock()
	if pending == nil {
		return false, fmt.Errorf("no MDBList authorization is pending")
	}
	if time.Now().After(pending.expiresAt) {
		c.clearPending()
		return false, fmt.Errorf("the code expired before it was entered")
	}

	body, status, err := c.postForm(ctx, "/oauth/token/", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {pending.deviceCode},
		"client_id":   {c.clientID},
	})
	if err != nil {
		return false, err
	}
	if status == http.StatusOK {
		state, err := tokenFromResponse(body)
		if err != nil {
			return false, err
		}
		// Best-effort: the name only labels the settings card, so a failed
		// lookup must not fail the link itself.
		if name, err := c.fetchUserName(ctx, state.AccessToken); err == nil {
			state.UserName = name
		} else {
			logger.Debug("MDBList user lookup failed after linking", "err", err)
		}
		c.persist(state)
		c.clearPending()
		logger.Info("MDBList account linked", "stream", c.stream, "user", state.UserName)
		return true, nil
	}

	switch oauthErrorCode(body) {
	case "authorization_pending":
		return false, nil
	case "slow_down":
		// The caller already polls at the interval MDBList asked for, so this
		// is rare; treating it as pending keeps the next tick from erroring.
		logger.Debug("MDBList asked us to slow down device polling")
		return false, nil
	case "access_denied":
		c.clearPending()
		return false, fmt.Errorf("authorization was denied")
	case "expired_token":
		c.clearPending()
		return false, fmt.Errorf("the code expired before it was entered")
	}
	return false, fmt.Errorf("mdblist token request failed: %s", oauthErrorDetail(body, status))
}

func (c *Client) clearPending() {
	c.deviceMu.Lock()
	c.pending = nil
	c.deviceMu.Unlock()
}

// tokenFromResponse turns a token endpoint reply into the state to persist,
// converting the relative lifetime into an absolute expiry.
func tokenFromResponse(body []byte) (tokenState, error) {
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return tokenState{}, err
	}
	if out.AccessToken == "" {
		return tokenState{}, fmt.Errorf("mdblist returned no access token")
	}
	state := tokenState{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if out.ExpiresIn > 0 {
		state.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return state, nil
}

// accessToken returns a token good to use now, renewing first when the stored
// one has expired or is about to.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	current := c.snapshot()
	if current.AccessToken == "" {
		return "", fmt.Errorf("no MDBList account is linked")
	}
	if !current.expiringSoon() {
		return current.AccessToken, nil
	}
	return c.refresh(ctx, current.AccessToken)
}

// refresh renews the access token. stale is the token the caller found
// unusable: when the stored token has already moved past it another caller
// refreshed first, and that result is returned rather than spending the
// refresh token a second time.
func (c *Client) refresh(ctx context.Context, stale string) (string, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	current := c.snapshot()
	if current.AccessToken != "" && current.AccessToken != stale {
		return current.AccessToken, nil
	}
	if current.RefreshToken == "" {
		c.invalidate("the stored MDBList token expired and carries no refresh token")
		return "", fmt.Errorf("no MDBList refresh token is on file")
	}
	body, status, err := c.postForm(ctx, "/oauth/token/", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {current.RefreshToken},
		"client_id":     {c.clientID},
	})
	if err != nil {
		// A transient failure keeps the token: the next call tries again.
		return "", err
	}
	if status != http.StatusOK {
		if code := oauthErrorCode(body); code == "invalid_grant" || code == "invalid_request" {
			c.invalidate("MDBList rejected the refresh token")
			return "", fmt.Errorf("mdblist rejected the refresh token; the account needs re-linking")
		}
		return "", fmt.Errorf("mdblist token refresh failed: %s", oauthErrorDetail(body, status))
	}
	next, err := tokenFromResponse(body)
	if err != nil {
		return "", err
	}
	if next.RefreshToken == "" {
		// MDBList does not have to rotate it; keep the one that still works.
		next.RefreshToken = current.RefreshToken
	}
	next.UserName = current.UserName
	c.persist(next)
	logger.Debug("MDBList access token refreshed", "user", next.UserName)
	return next.AccessToken, nil
}

// invalidate drops a token MDBList definitively rejected, so the settings page
// shows disconnected instead of scrobbles that silently go nowhere.
func (c *Client) invalidate(reason string) {
	logger.Warn(reason+"; the MDBList account needs re-linking", "stream", c.stream)
	c.dropToken()
}

type userResponse struct {
	UserName string `json:"username"`
	Name     string `json:"name"`
}

func (c *Client) fetchUserName(ctx context.Context, token string) (string, error) {
	var out userResponse
	if err := c.request(ctx, http.MethodGet, "/user", token, nil, &out); err != nil {
		return "", err
	}
	if name := strings.TrimSpace(out.UserName); name != "" {
		return name, nil
	}
	return strings.TrimSpace(out.Name), nil
}

// scrobblePayload renders the request body for one item, or reports why
// MDBList cannot place it. MDBList addresses everything by the aired series —
// anime included — so an anime entry that anime-lists could not resolve onto
// aired ids and numbering has no address here even when Simkl has one.
func scrobblePayload(item scrobble.Item, progress float64) (map[string]any, error) {
	body := map[string]any{"progress": scrobble.ClampProgress(progress)}
	ids := map[string]any{}
	if item.IMDbID != "" {
		ids["imdb"] = item.IMDbID
	}
	switch {
	case item.ContentType == "anime" && item.AnimeMovie:
		// An anime film is placed by its IMDb id alone. anime-lists prefers an
		// entry's TMDB *series* record when it publishes both, so its TMDB id
		// cannot be trusted to name a movie — and a film filed against the
		// wrong record writes history the account never watched.
		if len(ids) == 0 {
			return nil, fmt.Errorf("anime film scrobble needs an imdb id")
		}
		body["movie"] = map[string]any{"ids": ids}
	case item.ContentType == "movie":
		if item.TMDBID != "" {
			ids["tmdb"] = scrobble.IDValue(item.TMDBID)
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("movie scrobble needs an imdb or tmdb id")
		}
		body["movie"] = map[string]any{"ids": ids}
	default:
		if item.TMDBID != "" {
			ids["tmdb"] = scrobble.IDValue(item.TMDBID)
		}
		if item.TVDBID != "" {
			ids["tvdb"] = scrobble.IDValue(item.TVDBID)
		}
		if len(ids) == 0 || item.Season < 1 || item.Episode < 1 {
			return nil, fmt.Errorf("show scrobble needs a show id and an aired season/episode")
		}
		body["show"] = map[string]any{"ids": ids, "season": item.Season, "episode": item.Episode}
	}
	return body, nil
}

// Supports reports whether the item can be addressed on MDBList at all.
func (c *Client) Supports(item scrobble.Item) bool {
	_, err := scrobblePayload(item, 0)
	return err == nil
}

// Scrobble reports playback state for one item. Verb "start" opens a session
// — MDBList expires it by itself once the remaining runtime has elapsed — and
// "stop" ends it: past 80% progress the item is marked watched, below that the
// position is kept as a resumable playback.
func (c *Client) Scrobble(ctx context.Context, verb string, item scrobble.Item, progress float64) error {
	body, err := scrobblePayload(item, progress)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/scrobble/"+verb, body, nil)
}

// do runs one account request, renewing the token and retrying once if MDBList
// rejects it. A token can be refused before its recorded expiry — the user
// revoking the app looks exactly like that — so the 401 is worth one retry
// rather than an immediate unlink.
func (c *Client) do(ctx context.Context, method, path string, body any, target any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	err = c.request(ctx, method, path, token, body, target)
	if !isUnauthorized(err) {
		return err
	}
	refreshed, refreshErr := c.refresh(ctx, token)
	if refreshErr != nil {
		return refreshErr
	}
	if err := c.request(ctx, method, path, refreshed, body, target); err != nil {
		if isUnauthorized(err) {
			c.invalidate("MDBList rejected a freshly refreshed token")
		}
		return err
	}
	return nil
}

// unauthorizedError marks the one failure do() retries, so the check does not
// go looking for a status code in a message string.
type unauthorizedError struct{ status int }

func (e *unauthorizedError) Error() string {
	return fmt.Sprintf("mdblist rejected the access token (%d)", e.status)
}

func isUnauthorized(err error) bool {
	_, ok := err.(*unauthorizedError)
	return ok
}

// request runs one authenticated request with the token it is given, doing no
// renewal of its own — the refresh path uses it too and must not recurse.
func (c *Client) request(ctx context.Context, method, path, token string, body any, target any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &unauthorizedError{status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("MDBList %s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

// postForm runs one unauthenticated OAuth call. The body comes back undecoded
// because the device flow's "not yet" answers arrive as 400s carrying an error
// code that the caller, not the transport, has to interpret.
func (c *Client) postForm(ctx context.Context, path string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return body, resp.StatusCode, err
}

func oauthErrorCode(body []byte) string {
	var out struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &out) != nil {
		return ""
	}
	return out.Error
}

func oauthErrorDetail(body []byte, status int) string {
	var out struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &out) == nil && out.Error != "" {
		if out.Description != "" {
			return out.Error + ": " + out.Description
		}
		return out.Error
	}
	return fmt.Sprintf("status %d", status)
}
