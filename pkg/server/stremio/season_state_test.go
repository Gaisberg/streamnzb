package stremio

import (
	"testing"
	"time"

	"streamnzb/pkg/services/metadata/tvmaze"
)

func TestSeasonScanCompletion(t *testing.T) {
	past := time.Now().AddDate(0, 0, -30).Format(airDateLayout)
	future := time.Now().AddDate(0, 0, 30).Format(airDateLayout)

	cases := []struct {
		name          string
		airdates      []string
		wantCompleted bool
		wantKnown     bool
	}{
		{"no episodes listed", nil, false, false},
		{"every episode aired", []string{past, past, past}, true, true},
		{"finale still ahead", []string{past, past, future}, false, true},
		{"undated episode keeps season ongoing", []string{past, ""}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scan := newSeasonScan()
			for _, airdate := range tc.airdates {
				scan.add(dateWindow(airdate))
			}
			completed, known := scan.result()
			if completed != tc.wantCompleted || known != tc.wantKnown {
				t.Fatalf("result() = (%t, %t), want (%t, %t)", completed, known, tc.wantCompleted, tc.wantKnown)
			}
		})
	}
}

// A scheduled broadcast instant is what the season scan reads for a TVMaze
// episode that has one — the same window the unaired gate uses.
func TestTVMazeEpisodeWindowUsesScheduledInstant(t *testing.T) {
	scheduled := time.Now().Add(48 * time.Hour).UTC()
	ep := tvmaze.Episode{
		Season:   3,
		Number:   7,
		Airdate:  scheduled.Format(airDateLayout),
		Airtime:  "21:00",
		Airstamp: scheduled.Format(time.RFC3339),
	}
	w, ok := tvmazeEpisodeWindow(ep)
	if !ok {
		t.Fatal("tvmazeEpisodeWindow() reported no window")
	}
	if !w.scheduled.Equal(scheduled.Truncate(time.Second)) {
		t.Fatalf("scheduled = %v, want %v", w.scheduled, scheduled)
	}
	scan := newSeasonScan()
	scan.add(w, ok)
	if completed, known := scan.result(); completed || !known {
		t.Fatalf("result() = (%t, %t), want (false, true) for an unaired finale", completed, known)
	}
}

func TestSeasonCompletedStateUnknownWithoutSources(t *testing.T) {
	srv := &Server{}
	if _, known := srv.seasonCompletedState(t.Context(), "series", nil); known {
		t.Fatal("expected unknown season state without content ids")
	}
}
