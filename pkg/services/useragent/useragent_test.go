package useragent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// stubResolver points a resolver at a fake GitHub plus a fake VLC manifest.
// tags maps "owner/name" to the tag the stub reports; a repo missing from the
// map answers 404 so partial-failure handling can be exercised.
func stubResolver(t *testing.T, tags map[string]string, vlcBody string) (*Resolver, *int32) {
	t.Helper()
	var calls int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/releases/latest")
		tag, ok := tags[repo]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	}))
	t.Cleanup(github.Close)

	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, vlcBody)
	}))
	t.Cleanup(status.Close)

	r := NewResolver(github.Client())
	r.GitHubAPIBase = github.URL
	r.StatusURLOverride = status.URL
	return r, &calls
}

func allTags() map[string]string {
	return map[string]string{
		"Prowlarr/Prowlarr": "v2.5.2.5491",
		"Sonarr/Sonarr":     "v4.0.19.2979",
		"Radarr/Radarr":     "v6.3.0.10514",
		"sabnzbd/sabnzbd":   "5.1.1",
		"nzbgetcom/nzbget":  "v26.2",
	}
}

func agentByID(res Result, id string) (Agent, bool) {
	for _, a := range res.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

func TestLatestBuildsUserAgents(t *testing.T) {
	r, _ := stubResolver(t, allTags(), "3.0.23\nhttp://get.videolan.org/vlc/3.0.23/win64/vlc.exe\n")
	res, err := r.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	want := map[string]string{
		"prowlarr": "Prowlarr/2.5.2.5491",
		"sonarr":   "Sonarr/4.0.19.2979",
		"radarr":   "Radarr/6.3.0.10514",
		"sabnzbd":  "SABnzbd/5.1.1",
		// NZBGet writes its product token lowercase.
		"nzbget": "nzbget/26.2",
		// VLC comes from the first line of the updater manifest.
		"vlc": "VLC/3.0.23",
	}
	for id, ua := range want {
		got, ok := agentByID(res, id)
		if !ok {
			t.Errorf("%s missing from result", id)
			continue
		}
		if got.UserAgent != ua {
			t.Errorf("%s user agent = %q, want %q", id, got.UserAgent, ua)
		}
	}
}

func TestLatestRoleDefaultsFollowCatalogOrder(t *testing.T) {
	r, _ := stubResolver(t, allTags(), "3.0.23\n")
	res, err := r.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	// The first agent of each role is what an empty header is filled with, so
	// the order the settings page relies on is part of the contract.
	first := map[string]string{}
	for _, a := range res.Agents {
		if _, seen := first[a.Role]; !seen {
			first[a.Role] = a.ID
		}
	}
	for role, want := range map[string]string{RoleQuery: "prowlarr", RoleGrab: "sabnzbd", RoleProvider: "vlc"} {
		if first[role] != want {
			t.Errorf("default for role %q = %q, want %q", role, first[role], want)
		}
	}
}

func TestLatestKeepsPartialResults(t *testing.T) {
	tags := allTags()
	delete(tags, "nzbgetcom/nzbget")
	r, _ := stubResolver(t, tags, "3.0.23\n")
	res, err := r.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if _, ok := agentByID(res, "nzbget"); ok {
		t.Error("nzbget resolved despite a 404")
	}
	if _, ok := agentByID(res, "sabnzbd"); !ok {
		t.Error("one failing source dropped the others")
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "nzbget") {
		t.Errorf("errors = %v, want one nzbget entry", res.Errors)
	}
}

func TestLatestFailsWhenNothingResolves(t *testing.T) {
	r, _ := stubResolver(t, nil, "not-a-version\n")
	if _, err := r.Latest(context.Background()); err == nil {
		t.Fatal("expected an error when every source fails")
	}
}

func TestLatestServesFromCache(t *testing.T) {
	r, calls := stubResolver(t, allTags(), "3.0.23\n")
	if _, err := r.Latest(context.Background()); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	after := atomic.LoadInt32(calls)
	if _, err := r.Latest(context.Background()); err != nil {
		t.Fatalf("Latest (cached): %v", err)
	}
	if got := atomic.LoadInt32(calls); got != after {
		t.Errorf("second Latest made %d extra upstream requests, want 0", got-after)
	}
}

func TestCleanVersionRejectsNonVersions(t *testing.T) {
	for _, raw := range []string{"testing", "", "v", "nightly-2026-08-18", "1"} {
		if v, err := cleanVersion(raw); err == nil {
			t.Errorf("cleanVersion(%q) = %q, want an error", raw, v)
		}
	}
	for raw, want := range map[string]string{"v26.2": "26.2", "5.1.1": "5.1.1", " v2.5.2.5491 ": "2.5.2.5491"} {
		got, err := cleanVersion(raw)
		if err != nil {
			t.Errorf("cleanVersion(%q): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("cleanVersion(%q) = %q, want %q", raw, got, want)
		}
	}
}
