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

func TestGetUsageRefreshesDailyCountersAfterRollover(t *testing.T) {
	usageManager := testEasynewsUsageManager(t)
	name := "easynews-rollover-usage"
	usageData := usageManager.GetIndexerUsage(name)
	usageData.LastResetDay = time.Now().Format("2006-01-02")
	usageData.APIHitsUsed = 8
	usageData.DownloadsUsed = 4
	usageData.AllTimeAPIHitsUsed = 30
	usageData.AllTimeDownloadsUsed = 12

	client, err := NewClient("user", "pass", name, "", 8, 4, 0, 0, usageManager)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	usageData.LastResetDay = time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	usageData.APIHitsUsed = 8
	usageData.DownloadsUsed = 4

	usage := client.GetUsage()
	if usage.APIHitsUsed != 0 || usage.DownloadsUsed != 0 {
		t.Fatalf("expected refreshed daily usage to reset, got hits=%d downloads=%d", usage.APIHitsUsed, usage.DownloadsUsed)
	}
	if usage.APIHitsRemaining != 8 || usage.DownloadsRemaining != 4 {
		t.Fatalf("expected refreshed remaining counts, got api=%d downloads=%d", usage.APIHitsRemaining, usage.DownloadsRemaining)
	}
	if usage.AllTimeAPIHitsUsed != 30 || usage.AllTimeDownloadsUsed != 12 {
		t.Fatalf("expected all-time usage unchanged, got hits=%d downloads=%d", usage.AllTimeAPIHitsUsed, usage.AllTimeDownloadsUsed)
	}
}

func TestLimitChecksRefreshDailyUsageAfterRollover(t *testing.T) {
	usageManager := testEasynewsUsageManager(t)
	name := "easynews-rollover-limits"
	usageData := usageManager.GetIndexerUsage(name)
	usageData.LastResetDay = time.Now().Format("2006-01-02")
	usageData.APIHitsUsed = 8
	usageData.DownloadsUsed = 4

	client, err := NewClient("user", "pass", name, "", 8, 4, 0, 0, usageManager)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	usageData.LastResetDay = time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	usageData.APIHitsUsed = 8
	usageData.DownloadsUsed = 4
	usageData.AllTimeAPIHitsUsed = 35
	usageData.AllTimeDownloadsUsed = 14

	if err := client.checkAPILimit(); err != nil {
		t.Fatalf("checkAPILimit() error = %v, want nil after rollover refresh", err)
	}
	if err := client.checkDownloadLimit(); err != nil {
		t.Fatalf("checkDownloadLimit() error = %v, want nil after rollover refresh", err)
	}
}

func TestBuildEasynewsGPSQuery(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		season   string
		episode  string
		scope    string
		category string
		want     string
	}{
		{
			name:     "tv param mode appends season and episode",
			query:    "The Last of Us",
			season:   "1",
			episode:  "2",
			scope:    config.SeriesSearchScopeSeasonEpisode,
			category: "5000",
			want:     "The Last of Us S01E02",
		},
		{
			name:     "tv query mode keeps prepared query unchanged",
			query:    "The Last of Us S01E02",
			season:   "1",
			episode:  "2",
			scope:    config.SeriesSearchScopeSeasonEpisode,
			category: "5000",
			want:     "The Last of Us S01E02",
		},
		{
			name:     "tv query with extra terms does not duplicate episode token",
			query:    "The Boys S05E01 1080p",
			season:   "5",
			episode:  "1",
			scope:    config.SeriesSearchScopeSeasonEpisode,
			category: "5000",
			want:     "The Boys S05E01 1080p",
		},
		{
			name:     "season param appends season only",
			query:    "The Last of Us",
			season:   "1",
			scope:    config.SeriesSearchScopeSeason,
			category: "5000",
			want:     "The Last of Us S01",
		},
		{
			name:     "season query keeps prepared season query unchanged",
			query:    "The Last of Us S01",
			season:   "1",
			scope:    config.SeriesSearchScopeSeason,
			category: "5000",
			want:     "The Last of Us S01",
		},
		{
			name:     "movie query unchanged",
			query:    "The Age of Adaline 2015",
			season:   "1",
			episode:  "2",
			scope:    config.SeriesSearchScopeSeasonEpisode,
			category: "2000",
			want:     "The Age of Adaline 2015",
		},
		{
			name:     "all 5xxx categories are treated as tv",
			query:    "The King Who Never Was",
			season:   "1",
			episode:  "1",
			scope:    config.SeriesSearchScopeSeasonEpisode,
			category: "5030",
			want:     "The King Who Never Was S01E01",
		},
		{
			name:     "trimmed tv category still appends suffix",
			query:    "The Last of Us",
			season:   "1",
			episode:  "2",
			scope:    config.SeriesSearchScopeSeasonEpisode,
			category: " 5000",
			want:     "The Last of Us S01E02",
		},
		{
			name:     "empty tv title returns episode suffix without leading space",
			query:    "",
			season:   "1",
			episode:  "2",
			scope:    config.SeriesSearchScopeSeasonEpisode,
			category: "5000",
			want:     "S01E02",
		},
		{
			name:     "normalizes german punctuation and umlauts",
			query:    "Bube, Dame, König, grAS",
			scope:    config.SeriesSearchScopeNone,
			category: "5000",
			want:     "Bube Dame Koenig grAS",
		},
		{
			name:     "normalizes original punctuation",
			query:    "Lock, Stock & Two Smoking Barrels",
			scope:    config.SeriesSearchScopeNone,
			category: "2000",
			want:     "Lock Stock Two Smoking Barrels",
		},
		{
			name:     "normalizes colon punctuation",
			query:    "Avatar: Fire and Ash",
			scope:    config.SeriesSearchScopeNone,
			category: "2000",
			want:     "Avatar Fire and Ash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildEasynewsGPSQuery(tt.query, tt.season, tt.episode, tt.scope, tt.category); got != tt.want {
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

func TestPrepareEasynewsQueryIncludesExtraSearchTerms(t *testing.T) {
	overrideTerms := "GERMAN"
	uhdTerms := "2160p"
	punctuatedTerms := "x265-GER"

	tests := []struct {
		name             string
		baseQuery        string
		searchMode       string
		overrides        *config.IndexerSearchConfig
		wantPreparedText string
	}{
		{
			name:             "text search appends override extra terms",
			baseQuery:        "Avatar Fire and Ash",
			searchMode:       "text",
			overrides:        &config.IndexerSearchConfig{ExtraSearchTerms: &uhdTerms},
			wantPreparedText: "Avatar Fire and Ash 2160p",
		},
		{
			name:             "id search prepends override extra terms",
			baseQuery:        "S01E01",
			searchMode:       "id",
			overrides:        &config.IndexerSearchConfig{ExtraSearchTerms: &overrideTerms},
			wantPreparedText: "GERMAN S01E01",
		},
		{
			name:             "without override leaves query unchanged",
			baseQuery:        "Lock Stock Two Smoking Barrels",
			searchMode:       "text",
			overrides:        nil,
			wantPreparedText: "Lock Stock Two Smoking Barrels",
		},
		{
			name:             "extra terms are not normalized",
			baseQuery:        "Bube, Dame, König, grAS",
			searchMode:       "text",
			overrides:        &config.IndexerSearchConfig{ExtraSearchTerms: &punctuatedTerms},
			wantPreparedText: "Bube Dame Koenig grAS x265-GER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prepareEasynewsQuery(tt.baseQuery, tt.searchMode, tt.overrides); got != tt.wantPreparedText {
				t.Fatalf("prepareEasynewsQuery() = %q, want %q", got, tt.wantPreparedText)
			}
		})
	}
}
