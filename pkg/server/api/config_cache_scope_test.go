package api

import "testing"

func TestCacheClearScopeForPatch(t *testing.T) {
	cases := []struct {
		name string
		body string
		want cacheClearScope
	}{
		{"no impact keys", `{"log_level":"DEBUG","session_ttl_minutes":30}`, cacheClearNone},
		{"playlist only", `{"availnzb_mode":"off"}`, cacheClearPlaylist},
		{"filter profiles", `{"filter_profiles":[]}`, cacheClearPlaylist},
		{"playlist plus no impact", `{"availnzb_mode":"on","mute_error_video":true}`, cacheClearPlaylist},
		{"indexers", `{"indexers":[]}`, cacheClearSearch},
		{"search queries", `{"movie_search_queries":[]}`, cacheClearSearch},
		{"unknown key defaults to search", `{"some_future_field":1}`, cacheClearSearch},
		{"playlist plus search", `{"filter_profiles":[],"providers":[]}`, cacheClearSearch},
		{"invalid body", `not json`, cacheClearSearch},
		{"empty body", ``, cacheClearSearch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheClearScopeForPatch([]byte(tc.body)); got != tc.want {
				t.Fatalf("scope = %d, want %d", got, tc.want)
			}
		})
	}
}
