package persistence

import (
	"fmt"
	"testing"
)

func TestSearchDiagnosticsRoundTrip(t *testing.T) {
	mgr := newTestStateManager(t)

	mgr.RecordSearchDiagnostic(SearchDiagnostic{
		StreamName:   "default",
		ContentType:  "movie",
		ContentID:    "tt27419466",
		ContentTitle: "Moana",
		Payload:      `{"total_ms":2306}`,
	})
	mgr.RecordSearchDiagnostic(SearchDiagnostic{
		StreamName:  "default",
		ContentType: "series",
		ContentID:   "tt3121722:1:1",
		Payload:     `{"total_ms":1081}`,
	})

	all, err := mgr.ListSearchDiagnostics(ListSearchDiagnosticsOptions{})
	if err != nil {
		t.Fatalf("ListSearchDiagnostics: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(all))
	}

	movies, err := mgr.ListSearchDiagnostics(ListSearchDiagnosticsOptions{ContentType: "movie", ContentID: "tt27419466"})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(movies) != 1 || movies[0].ContentTitle != "Moana" || movies[0].Payload != `{"total_ms":2306}` {
		t.Fatalf("filter returned wrong row: %+v", movies)
	}
	if movies[0].CreatedAt.IsZero() {
		t.Fatal("CreatedAt must be stamped on insert")
	}
}

func TestSearchDiagnosticsEmptyPayloadIsDropped(t *testing.T) {
	mgr := newTestStateManager(t)
	mgr.RecordSearchDiagnostic(SearchDiagnostic{ContentType: "movie", ContentID: "tt1"})
	list, err := mgr.ListSearchDiagnostics(ListSearchDiagnosticsOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a payload-less row must not be stored, got %d rows", len(list))
	}
}

func TestSearchDiagnosticsPruneKeepsNewestRows(t *testing.T) {
	mgr := newTestStateManager(t)
	for i := 0; i < searchDiagnosticsKeepRows+25; i++ {
		mgr.RecordSearchDiagnostic(SearchDiagnostic{
			ContentType: "movie",
			ContentID:   fmt.Sprintf("tt%d", i),
			Payload:     `{}`,
		})
	}
	list, err := mgr.ListSearchDiagnostics(ListSearchDiagnosticsOptions{Limit: 200})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected rows to survive pruning")
	}
	// The newest row must still be present; the oldest must be gone.
	newest := fmt.Sprintf("tt%d", searchDiagnosticsKeepRows+24)
	if list[0].ContentID != newest {
		t.Fatalf("newest row = %s, want %s", list[0].ContentID, newest)
	}
	old, err := mgr.ListSearchDiagnostics(ListSearchDiagnosticsOptions{ContentID: "tt0"})
	if err != nil {
		t.Fatalf("list oldest: %v", err)
	}
	if len(old) != 0 {
		t.Fatal("oldest row survived pruning past the retention bound")
	}
}

func TestDeleteSearchDiagnosticsScopesToStream(t *testing.T) {
	mgr := newTestStateManager(t)

	for _, stream := range []string{"living-room", "phone"} {
		mgr.RecordSearchDiagnostic(SearchDiagnostic{
			StreamName:  stream,
			ContentType: "movie",
			ContentID:   "tt1",
			Payload:     `{"total_ms":1}`,
		})
	}

	deleted, err := mgr.DeleteSearchDiagnostics("phone")
	if err != nil {
		t.Fatalf("DeleteSearchDiagnostics: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	rest, _ := mgr.ListSearchDiagnostics(ListSearchDiagnosticsOptions{})
	if len(rest) != 1 || rest[0].StreamName != "living-room" {
		t.Fatalf("wrong rows survived: %+v", rest)
	}

	if deleted, err := mgr.DeleteSearchDiagnostics(""); err != nil || deleted != 1 {
		t.Fatalf("clear all: deleted=%d err=%v", deleted, err)
	}
	if rest, _ := mgr.ListSearchDiagnostics(ListSearchDiagnosticsOptions{}); len(rest) != 0 {
		t.Fatalf("expected no diagnostics, got %d", len(rest))
	}
}
