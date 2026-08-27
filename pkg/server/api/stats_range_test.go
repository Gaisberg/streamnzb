package api

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseStatsRange(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantErr  bool
		wantFrom *time.Time
		wantTo   *time.Time
	}{
		{
			name:  "empty range is open-ended",
			query: "",
		},
		{
			name:     "date-only to widened to cover the whole day",
			query:    "from=2026-08-01&to=2026-08-02",
			wantFrom: timePtr(time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)),
			wantTo:   timePtr(time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local)),
		},
		{
			name:     "rfc3339 timestamps used as-is",
			query:    "from=2026-08-27T06:30:00Z&to=2026-08-27T18:30:00Z",
			wantFrom: timePtr(time.Date(2026, 8, 27, 6, 30, 0, 0, time.UTC)),
			wantTo:   timePtr(time.Date(2026, 8, 27, 18, 30, 0, 0, time.UTC)),
		},
		{
			name:     "rfc3339 with fractional seconds",
			query:    "from=2026-08-26T10:00:00.000Z",
			wantFrom: timePtr(time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)),
		},
		{
			name:    "garbage from rejected",
			query:   "from=yesterday",
			wantErr: true,
		},
		{
			name:    "inverted range rejected",
			query:   "from=2026-08-27T18:00:00Z&to=2026-08-27T06:00:00Z",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/stats/history?"+tt.query, nil)
			from, to, errMsg := parseStatsRange(req)
			if tt.wantErr {
				if errMsg == "" {
					t.Fatalf("expected error, got from=%v to=%v", from, to)
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("unexpected error: %s", errMsg)
			}
			if !equalTimePtr(from, tt.wantFrom) {
				t.Errorf("from = %v, want %v", from, tt.wantFrom)
			}
			if !equalTimePtr(to, tt.wantTo) {
				t.Errorf("to = %v, want %v", to, tt.wantTo)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func equalTimePtr(got, want *time.Time) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.Equal(*want)
}
