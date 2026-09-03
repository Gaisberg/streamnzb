package easynews

import (
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
)

func init() {
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

var (
	easynewsUsageManagerOnce sync.Once
	easynewsUsageManager     *indexer.UsageManager
	easynewsUsageManagerErr  error
)

func testEasynewsUsageManager(t *testing.T) *indexer.UsageManager {
	t.Helper()

	easynewsUsageManagerOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "streamnzb-easynews-usage-")
		if err != nil {
			easynewsUsageManagerErr = err
			return
		}
		stateMgr, err := persistence.GetManager(tempDir)
		if err != nil {
			easynewsUsageManagerErr = err
			return
		}
		easynewsUsageManager, easynewsUsageManagerErr = indexer.GetUsageManager(stateMgr)
	})
	if easynewsUsageManagerErr != nil {
		t.Fatalf("GetUsageManager: %v", easynewsUsageManagerErr)
	}
	return easynewsUsageManager
}

func TestGetUsageCountsOnlyHitsInsideTrailingWindow(t *testing.T) {
	usageManager := testEasynewsUsageManager(t)
	name := "easynews-rollover-usage"

	client, err := NewClient("user", "pass", name, "", 8, 4, 0, 0, "", "", "", usageManager)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	usageManager.RecordHits(name, 8, 4, time.Now().Add(-25*time.Hour))
	usageManager.RecordHits(name, 2, 1, time.Now())

	usage := client.GetUsage()
	if usage.APIHitsUsed != 2 || usage.DownloadsUsed != 1 {
		t.Fatalf("expected only hits inside the window, got hits=%d downloads=%d", usage.APIHitsUsed, usage.DownloadsUsed)
	}
	if usage.APIHitsRemaining != 6 || usage.DownloadsRemaining != 3 {
		t.Fatalf("expected refreshed remaining counts, got api=%d downloads=%d", usage.APIHitsRemaining, usage.DownloadsRemaining)
	}
	if usage.AllTimeAPIHitsUsed != 10 || usage.AllTimeDownloadsUsed != 5 {
		t.Fatalf("expected all-time usage to keep every hit, got hits=%d downloads=%d", usage.AllTimeAPIHitsUsed, usage.AllTimeDownloadsUsed)
	}
}

func TestLimitChecksRefreshUsageFromPersistedHits(t *testing.T) {
	usageManager := testEasynewsUsageManager(t)
	name := "easynews-rollover-limits"

	client, err := NewClient("user", "pass", name, "", 8, 4, 0, 0, "", "", "", usageManager)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Hits older than the trailing window never count against the budget.
	usageManager.RecordHits(name, 8, 4, time.Now().Add(-25*time.Hour))
	if err := client.checkAPILimit(); err != nil {
		t.Fatalf("checkAPILimit() error = %v, want nil for hits outside the window", err)
	}
	if err := client.checkDownloadLimit(); err != nil {
		t.Fatalf("checkDownloadLimit() error = %v, want nil for hits outside the window", err)
	}

	// Fresh hits recorded behind the client's back are picked up on refresh.
	usageManager.RecordHits(name, 0, 4, time.Now())
	if err := client.checkDownloadLimit(); err == nil {
		t.Fatal("checkDownloadLimit() = nil, want an error once persisted downloads spend the budget")
	}
}

func TestBuildEasynewsGPSQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		season  string
		episode string
		scope   string
		series  bool
		want    string
	}{
		{
			name:    "tv param mode appends season and episode",
			query:   "The Last of Us",
			season:  "1",
			episode: "2",
			scope:   config.SeriesSearchScopeSeasonEpisode,
			series:  true,
			want:    "The Last of Us S01E02",
		},
		{
			name:    "tv query mode keeps prepared query unchanged",
			query:   "The Last of Us S01E02",
			season:  "1",
			episode: "2",
			scope:   config.SeriesSearchScopeSeasonEpisode,
			series:  true,
			want:    "The Last of Us S01E02",
		},
		{
			name:    "tv query with extra terms does not duplicate episode token",
			query:   "The Boys S05E01 1080p",
			season:  "5",
			episode: "1",
			scope:   config.SeriesSearchScopeSeasonEpisode,
			series:  true,
			want:    "The Boys S05E01 1080p",
		},
		{
			name:   "season param appends season only",
			query:  "The Last of Us",
			season: "1",
			scope:  config.SeriesSearchScopeSeason,
			series: true,
			want:   "The Last of Us S01",
		},
		{
			name:   "season query keeps prepared season query unchanged",
			query:  "The Last of Us S01",
			season: "1",
			scope:  config.SeriesSearchScopeSeason,
			series: true,
			want:   "The Last of Us S01",
		},
		{
			name:    "movie query unchanged",
			query:   "The Age of Adaline 2015",
			season:  "1",
			episode: "2",
			scope:   config.SeriesSearchScopeSeasonEpisode,
			series:  false,
			want:    "The Age of Adaline 2015",
		},
		{
			name:    "any tv class gets the episode suffix",
			query:   "The King Who Never Was",
			season:  "1",
			episode: "1",
			scope:   config.SeriesSearchScopeSeasonEpisode,
			series:  true,
			want:    "The King Who Never Was S01E01",
		},
		{
			name:    "a tv request appends the suffix",
			query:   "The Last of Us",
			season:  "1",
			episode: "2",
			scope:   config.SeriesSearchScopeSeasonEpisode,
			series:  true,
			want:    "The Last of Us S01E02",
		},
		{
			name:    "empty tv title returns episode suffix without leading space",
			query:   "",
			season:  "1",
			episode: "2",
			scope:   config.SeriesSearchScopeSeasonEpisode,
			series:  true,
			want:    "S01E02",
		},
		{
			name:   "normalizes german punctuation and umlauts",
			query:  "Bube, Dame, König, grAS",
			scope:  config.SeriesSearchScopeNone,
			series: true,
			want:   "Bube Dame Koenig grAS",
		},
		{
			name:   "normalizes original punctuation",
			query:  "Lock, Stock & Two Smoking Barrels",
			scope:  config.SeriesSearchScopeNone,
			series: false,
			want:   "Lock Stock Two Smoking Barrels",
		},
		{
			name:   "normalizes colon punctuation",
			query:  "Avatar: Fire and Ash",
			scope:  config.SeriesSearchScopeNone,
			series: false,
			want:   "Avatar Fire and Ash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildEasynewsGPSQuery(tt.query, tt.season, tt.episode, tt.scope, tt.series); got != tt.want {
				t.Fatalf("buildEasynewsGPSQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeTitleForSearchQuery(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Bube, Dame, König, grAS", "Bube Dame Koenig grAS"},
		{"Lock, Stock & Two Smoking Barrels", "Lock Stock Two Smoking Barrels"},
		{"Avatar: Fire and Ash", "Avatar Fire and Ash"},
		{"Good Luck, Have Fun, Don't Die", "Good Luck Have Fun Dont Die"},
	}

	for _, tt := range tests {
		if got := release.NormalizeTitleForSearchQuery(tt.in); got != tt.want {
			t.Fatalf("NormalizeTitleForSearchQuery(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPrepareEasynewsQuery(t *testing.T) {
	got := prepareEasynewsQuery("Bube, Dame, König, grAS")
	want := "Bube Dame Koenig grAS"
	if got != want {
		t.Fatalf("prepareEasynewsQuery() = %q, want %q", got, want)
	}
}
