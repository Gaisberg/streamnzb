// Package simkl talks to the Simkl watch-tracking service. Unlike the other
// metadata clients it is authenticated per account, not per API key: the user
// links their Simkl account through the PIN device flow, and the resulting
// access token (which Simkl never expires) is persisted alongside the TVDB
// token in the state store.
//
// Simkl's API terms forbid hammering /sync/all-items — clients that fetch the
// full list repeatedly get their client id suspended. The whole watchlist is
// therefore cached in memory and only refetched when the cheap
// /sync/activities probe reports a change, itself asked at most once per
// activitiesCheckInterval.
package simkl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

const (
	stateKey = "simkl_token"

	// activitiesCheckInterval throttles the change probe. Within the window
	// every catalog request serves from the cached list; after it, one
	// /sync/activities call decides whether the list is refetched.
	activitiesCheckInterval = time.Minute
)

// PosterURL renders a Simkl poster fragment ("74/74415673dcdc9cdd") as a
// medium poster image URL, or "" when the item has no artwork.
func PosterURL(poster string) string {
	poster = strings.TrimSpace(poster)
	if poster == "" {
		return ""
	}
	return "https://simkl.in/posters/" + poster + "_m.webp"
}

// credentialFingerprint pins a stored token to the client id it was authorized
// for, without persisting the id itself. Tokens are minted per Simkl app, so a
// token from a replaced client id cannot speak for the one in use now.
func credentialFingerprint(clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(clientID))
	return hex.EncodeToString(sum[:])
}

type tokenState struct {
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
	// Fingerprint pins the token to the client id it was authorized for.
	Fingerprint string `json:"fingerprint,omitempty"`
	// UserName is the display name captured when the account was linked, so
	// the settings page can say who is connected without an API call.
	UserName string `json:"user_name,omitempty"`
}

type Client struct {
	clientID string
	dataDir  string
	client   *http.Client
	BaseURL  string

	// tokenMu guards the persisted-token mirror; connect and disconnect race
	// against catalog requests on the shared client.
	tokenMu     sync.Mutex
	tokenLoaded bool
	tokenCache  string
	userName    string

	// listMu serializes watchlist refreshes: a board load fires every Simkl
	// row concurrently, and only the first may hit the API.
	listMu            sync.Mutex
	items             map[string][]Entry // simkl type ("shows"|"movies"|"anime") → entries
	listStamp         string             // activities "all" the cache was built from
	lastActivityCheck time.Time
}

func NewClient(clientID, dataDir string) *Client {
	baseURL := "https://api.simkl.com"
	if envURL := os.Getenv("STREAMNZB_SIMKL_BASE_URL"); envURL != "" {
		baseURL = envURL
	}
	return &Client{
		clientID: strings.TrimSpace(clientID),
		dataDir:  dataDir,
		// A large completed list is megabytes of JSON, so the timeout is
		// looser than the other metadata clients'.
		client:  &http.Client{Timeout: 30 * time.Second},
		BaseURL: baseURL,
	}
}

// ClientID reports the id the client was built with, so a reload can keep the
// instance — and its caches — when the effective id did not change.
func (c *Client) ClientID() string {
	if c == nil {
		return ""
	}
	return c.clientID
}

// Enabled reports whether a client id is available at all. Without one the PIN
// flow cannot start and the Simkl card offers configuration instead.
func (c *Client) Enabled() bool {
	return c != nil && c.clientID != ""
}

// Connected reports whether a linked account's token is on file.
func (c *Client) Connected() bool {
	token, _ := c.token()
	return token != ""
}

// UserName is the linked account's display name, or "" when disconnected or
// unknown.
func (c *Client) UserName() string {
	if c == nil {
		return ""
	}
	_, name := c.token()
	return name
}

// token returns the persisted access token and user name, loading them from
// the state store on first use. A token minted under a different client id is
// ignored — the account has to be re-linked.
func (c *Client) token() (string, string) {
	if c == nil || c.clientID == "" {
		return "", ""
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.tokenLoaded {
		return c.tokenCache, c.userName
	}
	c.tokenLoaded = true
	manager, err := persistence.GetManager(c.dataDir)
	if err != nil {
		return "", ""
	}
	var stored tokenState
	if found, _ := manager.Get(stateKey, &stored); found && stored.Token != "" {
		if stored.Fingerprint == credentialFingerprint(c.clientID) {
			c.tokenCache = stored.Token
			c.userName = stored.UserName
		} else {
			logger.Debug("Simkl token was authorized for a different client id; account needs re-linking")
		}
	}
	return c.tokenCache, c.userName
}

func (c *Client) setToken(token, userName string) {
	state := tokenState{
		Token:       token,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Fingerprint: credentialFingerprint(c.clientID),
		UserName:    userName,
	}
	if manager, err := persistence.GetManager(c.dataDir); err == nil {
		if err := manager.Set(stateKey, state); err != nil {
			logger.Warn("Failed to save Simkl token to state", "err", err)
		}
	}
	c.tokenMu.Lock()
	c.tokenLoaded = true
	c.tokenCache = token
	c.userName = userName
	c.tokenMu.Unlock()
	c.clearList()
}

// Disconnect unlinks the account: the persisted token and the cached watchlist
// are both dropped.
func (c *Client) Disconnect() {
	if c == nil {
		return
	}
	c.dropToken()
	c.clearList()
}

// dropToken clears the persisted and in-memory token without touching the list
// cache — the 401 path already holds listMu and clears the list itself.
func (c *Client) dropToken() {
	if manager, err := persistence.GetManager(c.dataDir); err == nil {
		_ = manager.Set(stateKey, tokenState{})
	}
	c.tokenMu.Lock()
	c.tokenLoaded = true
	c.tokenCache = ""
	c.userName = ""
	c.tokenMu.Unlock()
}

// invalidateToken drops a token Simkl definitively rejected (401 — the user
// revoked the app), so the settings page shows disconnected instead of rows
// that silently serve nothing. Only that definitive rejection lands here;
// transient failures keep the token.
func (c *Client) invalidateToken() {
	logger.Warn("Simkl rejected the access token; the account needs re-linking")
	c.dropToken()
}

func (c *Client) clearList() {
	c.listMu.Lock()
	c.items = nil
	c.listStamp = ""
	c.lastActivityCheck = time.Time{}
	c.listMu.Unlock()
}

// PinStart is one started PIN authorization: the code the user enters at the
// verification URL, and the polling contract for CheckPIN.
type PinStart struct {
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// StartPIN begins the device-code flow and returns the code to display.
func (c *Client) StartPIN(ctx context.Context) (*PinStart, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("no Simkl client id is configured")
	}
	var out struct {
		Result string `json:"result"`
		PinStart
	}
	if err := c.getJSON(ctx, "/oauth/pin?client_id="+c.clientID, "", &out); err != nil {
		return nil, err
	}
	if out.Result != "OK" || out.UserCode == "" {
		return nil, fmt.Errorf("simkl PIN request failed: result=%s", out.Result)
	}
	if out.VerificationURL == "" {
		out.VerificationURL = "https://simkl.com/pin/"
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return &out.PinStart, nil
}

// CheckPIN polls one started PIN authorization. It returns true once the user
// has approved the app — at which point the token is already persisted and the
// account is linked — and false while authorization is still pending.
func (c *Client) CheckPIN(ctx context.Context, userCode string) (bool, error) {
	userCode = strings.TrimSpace(userCode)
	if !c.Enabled() || userCode == "" {
		return false, fmt.Errorf("no Simkl client id is configured")
	}
	var out struct {
		Result      string `json:"result"`
		Message     string `json:"message"`
		AccessToken string `json:"access_token"`
	}
	if err := c.getJSON(ctx, "/oauth/pin/"+userCode+"?client_id="+c.clientID, "", &out); err != nil {
		return false, err
	}
	if out.Result != "OK" || out.AccessToken == "" {
		return false, nil
	}
	// Best-effort: the name only labels the settings card, so a failed lookup
	// must not fail the link itself.
	name := ""
	if settings, err := c.userSettings(ctx, out.AccessToken); err == nil {
		name = settings.User.Name
	} else {
		logger.Debug("Simkl user settings lookup failed after linking", "err", err)
	}
	c.setToken(out.AccessToken, name)
	logger.Info("Simkl account linked", "user", name)
	return true, nil
}

type userSettingsResponse struct {
	User struct {
		Name string `json:"name"`
	} `json:"user"`
}

func (c *Client) userSettings(ctx context.Context, token string) (*userSettingsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/users/settings", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("simkl-api-key", c.clientID)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("simkl users/settings returned status %d", resp.StatusCode)
	}
	var out userSettingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Entry is one watchlist item, flattened to what a catalog row needs.
type Entry struct {
	Title  string
	Poster string
	Year   int
	Status string
	IMDbID string
	TMDBID string
	TVDBID string
	MALID  string

	// sortTime orders rows newest-activity-first: last watched when the user
	// is working through it, list-add time otherwise.
	sortTime time.Time
}

// flexID tolerates Simkl's inconsistently typed ids — the same field arrives
// as a JSON string on one item and a number on the next.
type flexID string

func (f *flexID) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "null" {
		s = ""
	}
	*f = flexID(s)
	return nil
}

type mediaInfo struct {
	Title  string `json:"title"`
	Poster string `json:"poster"`
	Year   int    `json:"year"`
	IDs    struct {
		IMDb flexID `json:"imdb"`
		TMDB flexID `json:"tmdb"`
		TVDB flexID `json:"tvdb"`
		MAL  flexID `json:"mal"`
	} `json:"ids"`
}

type listItem struct {
	AddedToWatchlistAt string     `json:"added_to_watchlist_at"`
	LastWatchedAt      string     `json:"last_watched_at"`
	Status             string     `json:"status"`
	Show               *mediaInfo `json:"show"`
	Movie              *mediaInfo `json:"movie"`
}

type allItemsResponse struct {
	Shows  []listItem `json:"shows"`
	Anime  []listItem `json:"anime"`
	Movies []listItem `json:"movies"`
}

func (item listItem) toEntry() (Entry, bool) {
	media := item.Show
	if media == nil {
		media = item.Movie
	}
	if media == nil || media.Title == "" {
		return Entry{}, false
	}
	e := Entry{
		Title:  media.Title,
		Poster: media.Poster,
		Year:   media.Year,
		Status: item.Status,
		IMDbID: string(media.IDs.IMDb),
		TMDBID: string(media.IDs.TMDB),
		TVDBID: string(media.IDs.TVDB),
		MALID:  string(media.IDs.MAL),
	}
	if t, err := time.Parse(time.RFC3339, item.LastWatchedAt); err == nil {
		e.sortTime = t
	}
	if t, err := time.Parse(time.RFC3339, item.AddedToWatchlistAt); err == nil && t.After(e.sortTime) {
		e.sortTime = t
	}
	return e, true
}

// Watchlist returns the linked account's items of one Simkl type ("shows",
// "movies" or "anime") and status ("watching", "plantowatch", "hold",
// "completed", "dropped"), newest activity first. Served from the cached full
// list; see the package comment for the refresh policy.
func (c *Client) Watchlist(ctx context.Context, simklType, status string) ([]Entry, error) {
	token, _ := c.token()
	if token == "" {
		return nil, fmt.Errorf("no Simkl account is linked")
	}

	c.listMu.Lock()
	defer c.listMu.Unlock()
	if err := c.refreshListLocked(ctx, token); err != nil {
		if current, _ := c.token(); current == "" {
			// The refresh discovered the token was revoked — the stale list
			// no longer speaks for anyone.
			c.items = nil
			c.listStamp = ""
			return nil, err
		}
		if c.items == nil {
			return nil, err
		}
		// A stale list beats an empty board while Simkl hiccups.
		logger.Debug("Simkl watchlist refresh failed; serving cached list", "err", err)
	}
	var out []Entry
	for _, e := range c.items[simklType] {
		if e.Status == status {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].sortTime.After(out[j].sortTime) })
	return out, nil
}

// refreshListLocked brings the cached list up to date: a fresh cache is left
// alone, an aged one is revalidated against /sync/activities, and the full
// list is refetched only when that probe reports a change (or on first use).
func (c *Client) refreshListLocked(ctx context.Context, token string) error {
	if c.items != nil && time.Since(c.lastActivityCheck) < activitiesCheckInterval {
		return nil
	}
	stamp, err := c.fetchActivityStamp(ctx, token)
	if err != nil {
		return err
	}
	c.lastActivityCheck = time.Now()
	if c.items != nil && stamp == c.listStamp {
		return nil
	}

	var resp allItemsResponse
	if err := c.getJSON(ctx, "/sync/all-items/", token, &resp); err != nil {
		return err
	}
	items := make(map[string][]Entry, 3)
	for simklType, raw := range map[string][]listItem{"shows": resp.Shows, "movies": resp.Movies, "anime": resp.Anime} {
		entries := make([]Entry, 0, len(raw))
		for _, item := range raw {
			if e, ok := item.toEntry(); ok {
				entries = append(entries, e)
			}
		}
		items[simklType] = entries
	}
	c.items = items
	c.listStamp = stamp
	logger.Debug("Simkl watchlist refreshed",
		"shows", len(items["shows"]), "movies", len(items["movies"]), "anime", len(items["anime"]))
	return nil
}

func (c *Client) fetchActivityStamp(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/sync/activities", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("simkl-api-key", c.clientID)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		c.invalidateToken()
		return "", fmt.Errorf("simkl rejected the access token (401)")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("simkl activities returned status %d", resp.StatusCode)
	}
	var out struct {
		All string `json:"all"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.All, nil
}

// ScrobbleItem addresses one played title for the scrobble endpoints. Exactly
// one addressing shape is sent: movies by imdb/tmdb id, series by show ids
// plus aired season/episode, anime by MAL id plus the entry-local episode
// number (MAL and Kitsu entries share per-entry numbering).
type ScrobbleItem struct {
	ContentType string // "movie" | "series" | "anime"
	Title       string
	IMDbID      string
	TMDBID      string
	TVDBID      string
	MALID       string
	Season      int
	Episode     int
}

// scrobbleIDValue renders one id for the payload: Simkl's examples send
// numeric ids as numbers, so parseable ones go out that way.
func scrobbleIDValue(id string) interface{} {
	if n, err := strconv.Atoi(id); err == nil {
		return n
	}
	return id
}

// Scrobble reports playback state for one item: verb "start" marks it
// watching-now, verb "stop" ends the session — Simkl itself marks the item
// watched at ≥80% progress and saves a resumable playback below that. A 409
// (duplicate within Simkl's protection window) counts as success.
func (c *Client) Scrobble(ctx context.Context, verb string, item ScrobbleItem, progress float64) error {
	token, _ := c.token()
	if token == "" {
		return fmt.Errorf("no Simkl account is linked")
	}
	progress = math.Round(math.Min(100, math.Max(0, progress))*100) / 100

	body := map[string]interface{}{"progress": progress}
	ids := map[string]interface{}{}
	switch item.ContentType {
	case "movie":
		if item.IMDbID != "" {
			ids["imdb"] = item.IMDbID
		}
		if item.TMDBID != "" {
			ids["tmdb"] = scrobbleIDValue(item.TMDBID)
		}
		if len(ids) == 0 {
			return fmt.Errorf("movie scrobble needs an imdb or tmdb id")
		}
		movie := map[string]interface{}{"ids": ids}
		if item.Title != "" {
			movie["title"] = item.Title
		}
		body["movie"] = movie
	case "anime":
		if item.MALID == "" {
			return fmt.Errorf("anime scrobble needs a MAL id")
		}
		ids["mal"] = scrobbleIDValue(item.MALID)
		body["anime"] = map[string]interface{}{"ids": ids}
		episode := item.Episode
		if episode < 1 {
			// Anime movies and specials have a single episode on Simkl.
			episode = 1
		}
		body["episode"] = map[string]int{"number": episode}
	default: // series
		if item.IMDbID != "" {
			ids["imdb"] = item.IMDbID
		}
		if item.TMDBID != "" {
			ids["tmdb"] = scrobbleIDValue(item.TMDBID)
		}
		if item.TVDBID != "" {
			ids["tvdb"] = scrobbleIDValue(item.TVDBID)
		}
		if len(ids) == 0 || item.Season < 1 || item.Episode < 1 {
			return fmt.Errorf("series scrobble needs a show id and season/episode")
		}
		body["show"] = map[string]interface{}{"ids": ids}
		body["episode"] = map[string]int{"season": item.Season, "number": item.Episode}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/scrobble/"+verb, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("simkl-api-key", c.clientID)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		return nil
	case resp.StatusCode == http.StatusConflict:
		// Already recorded within Simkl's duplicate-protection window.
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		c.invalidateToken()
		return fmt.Errorf("simkl rejected the access token (401)")
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return fmt.Errorf("simkl scrobble/%s returned status %d: %s", verb, resp.StatusCode, strings.TrimSpace(string(respBody)))
}

// getJSON runs one GET and decodes the reply. token may be empty for the
// unauthenticated PIN endpoints. A JSON "null" body — Simkl's empty watchlist
// — decodes as the zero value.
func (c *Client) getJSON(ctx context.Context, path, token string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("simkl-api-key", c.clientID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && token != "" {
		c.invalidateToken()
		return fmt.Errorf("simkl rejected the access token (401)")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("simkl %s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}
