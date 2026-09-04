package seadex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A reduced SeaDex response: one entry with a best release, the same group's
// lesser release, and an alternative from another group.
const sampleResponse = `{
	"page": 1, "perPage": 1, "totalItems": 1, "totalPages": 1,
	"items": [{
		"alID": 116589,
		"expand": {"trs": [
			{"releaseGroup": "koala", "isBest": true, "dualAudio": true, "tracker": "Nyaa"},
			{"releaseGroup": "Koala", "isBest": false, "dualAudio": false, "tracker": "AnimeBytes"},
			{"releaseGroup": "Commie", "isBest": false, "dualAudio": false, "tracker": "Nyaa"}
		]}
	}]
}`

const emptyResponse = `{"page": 1, "perPage": 1, "totalItems": 0, "totalPages": 0, "items": []}`

func stubClient(t *testing.T, body string, hits *int) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	c := NewClient(nil)
	c.BaseURL = ts.URL
	return c
}

func TestGetEntryParsesAndCaches(t *testing.T) {
	hits := 0
	c := stubClient(t, sampleResponse, &hits)

	entry, err := c.GetEntry(context.Background(), 116589)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if entry == nil || entry.AniListID != 116589 || len(entry.Torrents) != 3 {
		t.Fatalf("entry = %+v, want 3 torrents for 116589", entry)
	}
	if tr := entry.Torrents[0]; tr.ReleaseGroup != "koala" || !tr.IsBest || !tr.DualAudio || tr.Tracker != "Nyaa" {
		t.Fatalf("first torrent = %+v", tr)
	}

	// The same lookup is answered from the cache.
	if _, err := c.GetEntry(context.Background(), 116589); err != nil {
		t.Fatalf("cached GetEntry: %v", err)
	}
	if hits != 1 {
		t.Fatalf("fetches = %d, want 1", hits)
	}
}

// An anime SeaDex has not cataloged is an answer, not an error.
func TestGetEntryMissingIsNotAnError(t *testing.T) {
	hits := 0
	c := stubClient(t, emptyResponse, &hits)

	entry, err := c.GetEntry(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if entry != nil {
		t.Fatalf("entry = %+v, want nil for an uncataloged title", entry)
	}
}

func TestGetEntryRejectsBadInput(t *testing.T) {
	hits := 0
	c := stubClient(t, sampleResponse, &hits)
	if _, err := c.GetEntry(context.Background(), 0); err == nil {
		t.Fatal("expected an invalid anilist id to error")
	}
	var nilClient *Client
	if _, err := nilClient.GetEntry(context.Background(), 1); err == nil {
		t.Fatal("expected a nil client to error rather than panic")
	}
	if hits != 0 {
		t.Fatalf("fetches = %d, want 0", hits)
	}
}

// A group with both a best and a lesser release lands only in best: the
// stronger claim wins whichever order the torrents arrive in.
func TestGroupSetsBestWins(t *testing.T) {
	entry := &Entry{Torrents: []Torrent{
		{ReleaseGroup: "Koala", IsBest: false},
		{ReleaseGroup: "koala", IsBest: true},
		{ReleaseGroup: "Commie", IsBest: false},
		{ReleaseGroup: "  ", IsBest: true},
	}}
	best, alt := entry.GroupSets()
	if !best["koala"] || len(best) != 1 {
		t.Fatalf("best = %v, want exactly koala", best)
	}
	if !alt["commie"] || len(alt) != 1 {
		t.Fatalf("alt = %v, want exactly commie", alt)
	}

	nilBest, nilAlt := (*Entry)(nil).GroupSets()
	if len(nilBest) != 0 || len(nilAlt) != 0 {
		t.Fatal("a nil entry recommends nothing")
	}
}

func TestDualAudioGroups(t *testing.T) {
	entry := &Entry{Torrents: []Torrent{
		{ReleaseGroup: "Koala", IsBest: true, DualAudio: false},
		{ReleaseGroup: "Anime Time", IsBest: false, DualAudio: true},
		{ReleaseGroup: "LostYears", IsBest: true, DualAudio: true},
		{ReleaseGroup: "  ", DualAudio: true},
		// The best decides: a sub-only best is not made dual by a dual
		// alternative from the same group.
		{ReleaseGroup: "Judas", IsBest: true, DualAudio: false},
		{ReleaseGroup: "judas", IsBest: false, DualAudio: true},
		// ...and in the other order of appearance.
		{ReleaseGroup: "Arid", IsBest: false, DualAudio: true},
		{ReleaseGroup: "Arid", IsBest: true, DualAudio: false},
	}}
	dual := entry.DualAudioGroups()
	if !dual["anime time"] || !dual["lostyears"] || len(dual) != 2 {
		t.Fatalf("dual = %v, want exactly anime time and lostyears", dual)
	}
	if dual["judas"] || dual["arid"] {
		t.Fatalf("a group whose best release is sub-only must not read as dual audio: %v", dual)
	}
	if len((*Entry)(nil).DualAudioGroups()) != 0 {
		t.Fatal("a nil entry has no dual-audio groups")
	}
}
