// Package useragent resolves the current release version of the download
// clients and media players StreamNZB spoofs in its User-Agent headers.
// Indexers increasingly gate content on the client version, so a header pinned
// to whatever was current when it was typed goes stale silently — the Settings
// page refreshes it from here instead of the user chasing release pages.
package useragent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Roles map a tool to the header slot it belongs to by default. They only
// decide what an empty field is filled with; a field that already names a tool
// keeps that tool and only moves to its newer version.
const (
	RoleQuery    = "query"
	RoleGrab     = "grab"
	RoleProvider = "provider"
	// RoleSeries and RoleMovie seed the per-media headers: Sonarr for
	// series, Radarr for films, each used for both search and grab.
	RoleSeries = "series"
	RoleMovie  = "movie"
)

const (
	githubAPIBase = "https://api.github.com"
	// vlcStatusURL is VLC's updater manifest, whose first line is the current
	// version. VLC publishes no GitHub releases (the mirror's tags are build
	// noise), so the updater is the only machine-readable source.
	vlcStatusURL = "https://update.videolan.org/vlc/status-win-x64"

	// cacheTTL bounds how often the upstream APIs are hit. GitHub allows 60
	// unauthenticated requests per hour per IP across its whole API, and one
	// refresh costs one request per source, so a repeatedly clicked button
	// must not turn into a request each time.
	cacheTTL = time.Hour

	requestTimeout = 10 * time.Second
	// maxBody caps what is read from a source. A GitHub release payload
	// carries every asset, which for the *arr projects runs to tens of KB.
	maxBody = 1 << 20
)

// versionPattern accepts the dotted numeric versions every catalog entry
// publishes (4.5.5, 2.5.2.5491, 26.2). It rejects the rolling pre-release tags
// some projects keep beside their stable line ("testing"), which would
// otherwise be spliced into a header verbatim.
var versionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

// Agent is one spoofable tool at its current release.
type Agent struct {
	ID        string `json:"id"`
	Product   string `json:"product"`
	Version   string `json:"version"`
	UserAgent string `json:"user_agent"`
	Role      string `json:"role"`
}

// Result is one refresh: whatever resolved, plus the sources that did not.
// Partial results are useful — one unreachable project should not stop the
// other headers from being brought up to date.
type Result struct {
	Agents    []Agent   `json:"agents"`
	Errors    []string  `json:"errors,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

// source is a catalog entry: the product token the tool writes into its own
// User-Agent, and where its version is published.
type source struct {
	id      string
	product string
	role    string
	// repo is an owner/name pair whose latest non-prerelease GitHub release
	// carries the version. The *arr projects publish their develop builds as
	// prereleases, so releases/latest — not the release list — is the stable
	// line.
	repo string
	// statusURL replaces repo for projects that publish no GitHub releases.
	statusURL string
}

// catalog is ordered: the first entry for a role is what an empty header of
// that role is filled with.
//
// Product tokens come from each tool's own source rather than guesswork —
// UserAgentBuilder.cs for the *arr apps ("Prowlarr/{version}"), misc.py for
// SABnzbd ("SABnzbd/{version}"), WebDownloader.cpp for NZBGet (lowercase
// "nzbget/{version}").
var catalog = []source{
	{id: "prowlarr", product: "Prowlarr", role: RoleQuery, repo: "Prowlarr/Prowlarr"},
	{id: "sonarr", product: "Sonarr", role: RoleSeries, repo: "Sonarr/Sonarr"},
	{id: "radarr", product: "Radarr", role: RoleMovie, repo: "Radarr/Radarr"},
	{id: "sabnzbd", product: "SABnzbd", role: RoleGrab, repo: "sabnzbd/sabnzbd"},
	{id: "nzbget", product: "nzbget", role: RoleGrab, repo: "nzbgetcom/nzbget"},
	{id: "vlc", product: "VLC", role: RoleProvider, statusURL: vlcStatusURL},
}

// Resolver fetches and caches the catalog's current versions.
type Resolver struct {
	httpClient *http.Client
	// GitHubAPIBase and StatusURLOverride are exported so tests can point the
	// resolver at a stub instead of the live APIs.
	GitHubAPIBase     string
	StatusURLOverride string

	// fetchMu serializes refreshes so a burst of clicks makes one round of
	// upstream requests. It is never held together with mu, which guards only
	// the cached state and so is never held across IO.
	fetchMu sync.Mutex

	mu      sync.Mutex
	cached  Result
	fetched time.Time
}

func NewResolver(httpClient *http.Client) *Resolver {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &Resolver{httpClient: httpClient, GitHubAPIBase: githubAPIBase}
}

// shared is the process-wide resolver, so every caller reuses one cache.
var shared = NewResolver(nil)

// Latest resolves the catalog through the shared resolver.
func Latest(ctx context.Context) (Result, error) { return shared.Latest(ctx) }

// Latest returns the current version of every catalog entry, from cache while
// it is fresh. It fails only when nothing at all could be resolved.
func (r *Resolver) Latest(ctx context.Context) (Result, error) {
	if res, ok := r.cachedResult(); ok {
		return res, nil
	}
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()
	// A refresh may have completed while this call waited for the lock.
	if res, ok := r.cachedResult(); ok {
		return res, nil
	}
	res := r.fetchAll(ctx)
	if len(res.Agents) == 0 {
		return res, fmt.Errorf("no release versions could be resolved: %s", strings.Join(res.Errors, "; "))
	}
	r.store(res)
	return res, nil
}

func (r *Resolver) cachedResult() (Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cached.Agents) == 0 || time.Since(r.fetched) >= cacheTTL {
		return Result{}, false
	}
	return r.cached, true
}

func (r *Resolver) store(res Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cached = res
	r.fetched = time.Now()
}

// fetchAll resolves every source concurrently, keeping catalog order so the
// role defaults stay predictable.
func (r *Resolver) fetchAll(ctx context.Context) Result {
	versions := make([]string, len(catalog))
	failures := make([]string, len(catalog))
	var wg sync.WaitGroup
	for i, src := range catalog {
		wg.Add(1)
		go func(i int, src source) {
			defer wg.Done()
			version, err := r.resolve(ctx, src)
			if err != nil {
				failures[i] = fmt.Sprintf("%s: %v", src.product, err)
				return
			}
			versions[i] = version
		}(i, src)
	}
	wg.Wait()

	res := Result{FetchedAt: time.Now()}
	for i, src := range catalog {
		if failures[i] != "" {
			res.Errors = append(res.Errors, failures[i])
			continue
		}
		res.Agents = append(res.Agents, Agent{
			ID:        src.id,
			Product:   src.product,
			Version:   versions[i],
			UserAgent: src.product + "/" + versions[i],
			Role:      src.role,
		})
	}
	return res
}

func (r *Resolver) resolve(ctx context.Context, src source) (string, error) {
	if src.statusURL != "" {
		url := src.statusURL
		if r.StatusURLOverride != "" {
			url = r.StatusURLOverride
		}
		return r.statusVersion(ctx, url)
	}
	return r.githubVersion(ctx, src.repo)
}

// githubVersion reads the tag of a repository's latest stable release.
func (r *Resolver) githubVersion(ctx context.Context, repo string) (string, error) {
	base := r.GitHubAPIBase
	if base == "" {
		base = githubAPIBase
	}
	body, err := r.get(ctx, fmt.Sprintf("%s/repos/%s/releases/latest", base, repo), map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	})
	if err != nil {
		return "", err
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return "", fmt.Errorf("decode release: %w", err)
	}
	return cleanVersion(release.TagName)
}

// statusVersion reads the version from the first line of a plain-text manifest.
func (r *Resolver) statusVersion(ctx context.Context, url string) (string, error) {
	body, err := r.get(ctx, url, nil)
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(string(body), "\n")
	return cleanVersion(first)
}

func (r *Resolver) get(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A 403 here is almost always the unauthenticated rate limit; say so
		// rather than leaving the user to decode a bare status code.
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("rate limited by the release API (HTTP %d), try again later", resp.StatusCode)
		}
		return nil, fmt.Errorf("release lookup failed with HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// cleanVersion strips a leading "v" and rejects anything that is not a dotted
// numeric version.
func cleanVersion(raw string) (string, error) {
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "v"))
	if !versionPattern.MatchString(v) {
		return "", fmt.Errorf("unexpected version %q", strings.TrimSpace(raw))
	}
	return v, nil
}
