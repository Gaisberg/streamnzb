package stremio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/simkl"
)

// newLinkedSimklClient builds a Simkl client against a stub API with an
// account already linked, serving one small watchlist.
func newLinkedSimklClient(t *testing.T) *simkl.Client {
	t.Helper()
	logger.Init("ERROR")
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/pin/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result": "OK", "access_token": "test-token"}`))
	})
	mux.HandleFunc("/users/settings", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user": {"name": "Test User"}}`))
	})
	mux.HandleFunc("/sync/activities", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"all": "2024-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("/sync/all-items/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"shows": [
				{"added_to_watchlist_at": "2024-03-01T00:00:00Z", "status": "watching",
				 "show": {"title": "With IMDb", "poster": "11/11aa", "ids": {"imdb": "tt0000001", "tmdb": "100"}}},
				{"added_to_watchlist_at": "2024-02-01T00:00:00Z", "status": "watching",
				 "show": {"title": "TMDB Only", "ids": {"tmdb": 200}}},
				{"added_to_watchlist_at": "2024-01-01T00:00:00Z", "status": "watching",
				 "show": {"title": "TVDB Only", "ids": {"tvdb": "300"}}},
				{"added_to_watchlist_at": "2024-01-01T00:00:00Z", "status": "watching",
				 "show": {"title": "No IDs", "ids": {}}},
				{"added_to_watchlist_at": "2024-01-01T00:00:00Z", "status": "completed",
				 "show": {"title": "Wrong Status", "ids": {"imdb": "tt0000002"}}}
			],
			"anime": [
				{"added_to_watchlist_at": "2024-01-01T00:00:00Z", "status": "watching",
				 "show": {"title": "Anime With IMDb", "ids": {"imdb": "tt0000003", "mal": "1"}}},
				{"added_to_watchlist_at": "2024-01-01T00:00:00Z", "status": "watching",
				 "show": {"title": "Anime MAL Only", "ids": {"mal": "2"}}}
			],
			"movies": []
		}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dir, err := os.MkdirTemp("", "simkl_catalog_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	client := simkl.NewClient("test-client", dir)
	client.BaseURL = server.URL
	if connected, err := client.CheckPIN(context.Background(), "ABC12"); err != nil || !connected {
		t.Fatalf("link: %v, %v", connected, err)
	}
	return client
}

// TestSimklCatalog covers the id preference order (tt → tmdb: → tvdb:), the
// no-usable-id drop, and status filtering, through the real buildCatalog
// dispatch on a bare server.
func TestSimklCatalog(t *testing.T) {
	srv := &Server{simklClient: newLinkedSimklClient(t)}
	def, ok := catalogDefByID("simkl.watching.series")
	if !ok {
		t.Fatal("simkl.watching.series missing from the registry")
	}
	metas, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: "series", ID: def.ID})
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	wantIDs := []string{"tt0000001", "tmdb:200", "tvdb:300"}
	if len(metas) != len(wantIDs) {
		t.Fatalf("metas = %+v, want %d rows", metas, len(wantIDs))
	}
	for i, want := range wantIDs {
		if metas[i].ID != want {
			t.Fatalf("metas[%d].ID = %q, want %q", i, metas[i].ID, want)
		}
	}
	if metas[0].Name != "With IMDb" || metas[0].Poster != "https://simkl.in/posters/11/11aa_m.webp" {
		t.Fatalf("metas[0] = %+v", metas[0])
	}

	// Anime with no MAL→Kitsu mapping available (nil store): tt survives as
	// the fallback, MAL-only entries drop.
	animeDef, _ := catalogDefByID("simkl.watching.anime")
	anime, err := srv.buildCatalog(context.Background(), animeDef, catalogRequest{Type: "anime", ID: animeDef.ID})
	if err != nil {
		t.Fatalf("anime buildCatalog: %v", err)
	}
	if len(anime) != 1 || anime[0].ID != "tt0000003" {
		t.Fatalf("anime = %+v, want the tt fallback row only", anime)
	}

	// Paging: a skip past the end is an empty page, not an error.
	if metas, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: "series", ID: def.ID, Skip: 50}); err != nil || len(metas) != 0 {
		t.Fatalf("deep skip = %+v, %v", metas, err)
	}
}

// TestSimklRegistryRows pins the row set: one row per Simkl status per media
// type (movies have no watching/hold on Simkl), none of them default-enabled —
// they only surface once an account is linked.
func TestSimklRegistryRows(t *testing.T) {
	statusesByType := map[string][]string{}
	for _, def := range catalogRegistry {
		if def.Provider != "simkl" {
			continue
		}
		if def.DefaultEnabled {
			t.Errorf("%s is default-enabled; Simkl rows must be opt-in", def.ID)
		}
		if !def.SupportsSkip {
			t.Errorf("%s does not support skip", def.ID)
		}
		statusesByType[def.Type] = append(statusesByType[def.Type], def.Kind)
	}
	want := map[string][]string{
		"series": {"watching", "plantowatch", "hold", "completed", "dropped"},
		"anime":  {"watching", "plantowatch", "hold", "completed", "dropped"},
		"movie":  {"plantowatch", "completed", "dropped"},
	}
	for contentType, statuses := range want {
		got := statusesByType[contentType]
		if len(got) != len(statuses) {
			t.Fatalf("%s rows = %v, want %v", contentType, got, statuses)
		}
		for i, status := range statuses {
			if got[i] != status {
				t.Fatalf("%s rows = %v, want %v", contentType, got, statuses)
			}
		}
	}
}

// TestManifestDropsSimklWhenDisconnected pins the gating: a profile with Simkl
// rows toggled on renders them in the manifest only while an account is
// linked.
func TestManifestDropsSimklWhenDisconnected(t *testing.T) {
	srv := &Server{} // no simkl client at all — maximally disconnected
	if got := srv.unavailableCatalogProviders(); len(got) != 1 || got[0] != "simkl" {
		t.Fatalf("unavailableCatalogProviders = %v, want [simkl]", got)
	}

	profile := &config.MetadataProfileConfig{
		Catalogs: []config.CatalogToggle{
			{ID: "simkl.watching.series", Enabled: true},
			{ID: "tmdb.trending.movie", Enabled: true},
		},
	}
	for _, cat := range enabledCatalogs(profile, "simkl") {
		if cat.ID == "simkl.watching.series" {
			t.Fatal("simkl row rendered while the provider is unavailable")
		}
	}
	// Undropped, the row is there — the toggle itself survived.
	found := false
	for _, cat := range enabledCatalogs(profile) {
		if cat.ID == "simkl.watching.series" {
			found = true
		}
	}
	if !found {
		t.Fatal("simkl row missing without a drop filter")
	}
}
