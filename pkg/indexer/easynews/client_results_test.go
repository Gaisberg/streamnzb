package easynews

import (
	"encoding/json"
	"testing"
)

func decodeSearchResponse(t *testing.T, body string) easynewsSearchResponse {
	t.Helper()
	var data easynewsSearchResponse
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return data
}

func TestFilterAndMapResultsNamedObjectRow(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	data := decodeSearchResponse(t, `{"results":1,"data":[{
		"hash":"abc123","fn":"Dune.Part.Two.2024.1080p","extension":".mkv",
		"sig":"sig-1","rawSize":12884901888,"size":"12.0 GB","runtime":9360,
		"poster":"poster@example.com","timestamp":1700000000}]}`)

	results := client.filterAndMapResults(data, "dune", "", "", false)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	got := results[0]
	if got.Title != "Dune.Part.Two.2024.1080p.mkv" {
		t.Fatalf("Title = %q", got.Title)
	}
	if got.Size != 12884901888 {
		t.Fatalf("Size = %d, want 12884901888", got.Size)
	}
	if got.DurationSeconds != 9360 {
		t.Fatalf("DurationSeconds = %v, want 9360", got.DurationSeconds)
	}
	if got.GUID != "easynews-abc123" {
		t.Fatalf("GUID = %q", got.GUID)
	}
}

func TestFilterAndMapResultsPositionalKeyObjectRow(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Identity fields arrive under their positional index; size and runtime are
	// named. "12" is the video codec on object rows and must never be read as a
	// size, and "14" must never be read as a runtime — a bogus sub-60s runtime
	// would silently drop the row.
	data := decodeSearchResponse(t, `{"results":1,"data":[{
		"0":"abc123","10":"Dune.Part.Two.2024.1080p","11":".mkv","12":"H264",
		"14":"3","sig":"sig-1","rawSize":12884901888,"runtime":9360}]}`)

	results := client.filterAndMapResults(data, "dune", "", "", false)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Title != "Dune.Part.Two.2024.1080p.mkv" {
		t.Fatalf("Title = %q", results[0].Title)
	}
	if results[0].Size != 12884901888 {
		t.Fatalf("Size = %d, want 12884901888", results[0].Size)
	}
	if results[0].DurationSeconds != 9360 {
		t.Fatalf("DurationSeconds = %v, want 9360", results[0].DurationSeconds)
	}
}

func TestFilterAndMapResultsKeepsRowsWithoutRuntime(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// No runtime at all must not be mistaken for a too-short file.
	data := decodeSearchResponse(t, `{"results":1,"data":[{
		"hash":"abc123","fn":"Show.S01E02.1080p","extension":".mkv",
		"rawSize":900000000,"12":"H264","14":"3"}]}`)

	results := client.filterAndMapResults(data, "show", "1", "2", false)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (a missing runtime must not drop the row)", len(results))
	}
}

func TestFilterAndMapResultsHumanizedSizeOnly(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	data := decodeSearchResponse(t, `{"results":1,"data":[{
		"hash":"abc123","fn":"Dune","extension":".mkv","size":"1.5 GB","runtime":3600}]}`)

	results := client.filterAndMapResults(data, "dune", "", "", false)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if want := int64(1.5 * (1 << 30)); results[0].Size != want {
		t.Fatalf("Size = %d, want %d", results[0].Size, want)
	}
}

func TestFilterAndMapResultsArrayRowStillWorks(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	data := decodeSearchResponse(t, `{"results":1,"data":[[
		"abc123","","","","","","subject","poster","2024-01-02 03:04:05","",
		"Dune.Part.Two.2024.1080p",".mkv",12884901888,"",9360]]}`)

	results := client.filterAndMapResults(data, "dune", "", "", false)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Size != 12884901888 || results[0].DurationSeconds != 9360 {
		t.Fatalf("size = %d, duration = %v", results[0].Size, results[0].DurationSeconds)
	}
}

func TestFilterAndMapResultsDropsFlaggedRows(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	data := decodeSearchResponse(t, `{"results":3,"data":[
		{"hash":"a","fn":"Dune","extension":".mkv","rawSize":100,"runtime":3600,"passwd":true},
		{"hash":"b","fn":"Dune","extension":".mkv","rawSize":100,"runtime":3600,"virus":true},
		{"hash":"c","fn":"Dune","extension":".mkv","rawSize":100,"runtime":3600,"passwd":false,"virus":false}]}`)

	results := client.filterAndMapResults(data, "dune", "", "", false)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (password-protected and virus rows dropped)", len(results))
	}
	if results[0].GUID != "easynews-c" {
		t.Fatalf("GUID = %q, want the unflagged row", results[0].GUID)
	}
}

func TestParseHumanSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1.5 GB", int64(1.5 * (1 << 30))},
		{"700MB", 700 << 20},
		{"12.0 GiB", 12 << 30},
		{"4096", 4096},
		{"", 0},
		{"unknown", 0},
		{"0 GB", 0},
	}

	for _, tt := range tests {
		if got := parseHumanSize(tt.in); got != tt.want {
			t.Fatalf("parseHumanSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
