package stremio

import (
	"encoding/json"
	"strings"
	"testing"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/search/diag"
)

// The Moana case from the field logs: three releases survived search and the
// profile rejected every one. The debug row must exist and say why.
func TestBuildSearchDebugStreamExplainsFilteredToNothing(t *testing.T) {
	server, mgr := newBadReleaseTestServer(t)
	server.config = nil // buildSearchDebugStream must not need config

	snap := diag.Snapshot{
		Validation:   []diag.ValidationStat{{Request: "DefaultMovieText", Mode: "text", Raw: 9, Kept: 9}},
		DedupInput:   15,
		DedupOutput:  3,
		ProfileName:  "Default Profile",
		ProfileInput: 3,
		ProfileKept:  0,
		Rejected: []diag.RejectedRelease{
			{Title: "Moana.2026.CAM.x264", Reasons: []string{"attribute:cam"}},
			{Title: "Moana.2026.TS.x264", Reasons: []string{"trash"}},
			{Title: "Moana.2026.480p.WEB.x264", Reasons: []string{"resolution:480p"}},
		},
		IndexerCalls: []diag.IndexerCall{
			{Indexer: "DrunkenSlug", Mode: "text", DurationMS: 727, Results: 4},
			{Indexer: "Treasuremaps", Mode: "id", DurationMS: 170, Results: 1},
		},
		TotalMS: 2306,
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mgr.RecordSearchDiagnostic(persistence.SearchDiagnostic{
		StreamName:  "default",
		ContentType: "movie",
		ContentID:   "tt27419466",
		Payload:     string(payload),
	})

	key := StreamSlotKey{StreamID: "default", ContentType: "movie", ID: "tt27419466"}
	dbg := server.buildSearchDebugStream(key, "http://localhost:7000/token")
	if dbg == nil {
		t.Fatal("expected a debug stream for content with recorded diagnostics")
	}
	if !strings.Contains(dbg.URL, "/play/"+key.SlotPath(0)) || !strings.Contains(dbg.URL, "src=debug") {
		t.Fatalf("debug URL must target slot 0 with a marker query, got %q", dbg.URL)
	}
	desc := dbg.Description
	for _, want := range []string{"2306 ms", "profile 3→0", "attribute:cam ×1", "DrunkenSlug 727ms/4"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	if dbg.BehaviorHints == nil || dbg.BehaviorHints.BingeGroup != "streamnzb-debug" {
		t.Fatal("debug row must sit in its own binge group so autoplay never chains into it")
	}
}

func TestBuildSearchDebugStreamAbsentWithoutDiagnostics(t *testing.T) {
	server, _ := newBadReleaseTestServer(t)
	key := StreamSlotKey{StreamID: "default", ContentType: "movie", ID: "tt-never-searched"}
	if dbg := server.buildSearchDebugStream(key, "http://localhost:7000/t"); dbg != nil {
		t.Fatalf("expected no debug stream without diagnostics, got %+v", dbg)
	}
}

func TestSummarizeIndexerCallsOneModeSucceeding(t *testing.T) {
	lines := summarizeIndexerCalls([]diag.IndexerCall{
		{Indexer: "altHUB", Mode: "id", DurationMS: 100, Results: 0, Error: "timeout"},
		{Indexer: "altHUB", Mode: "text", DurationMS: 833, Results: 8},
	}, 6)
	if len(lines) != 1 {
		t.Fatalf("expected one aggregated line, got %v", lines)
	}
	if strings.Contains(lines[0], "✗") {
		t.Fatalf("an indexer with one successful mode must not read as failed: %q", lines[0])
	}
	if !strings.Contains(lines[0], "833ms/8") {
		t.Fatalf("expected aggregated timing/results, got %q", lines[0])
	}
}
