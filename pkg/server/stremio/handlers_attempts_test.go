package stremio

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/playback"
	"streamnzb/pkg/release"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
)

func TestRecordAttemptParamsSuccessUsesServedProviders(t *testing.T) {
	server := &Server{}
	sess := &session.Session{
		ID:           "stream:global:series:tt2261227:2:2:2",
		StreamName:   "Stream04",
		ContentType:  "series",
		ContentID:    "tt2261227:2:2",
		ContentTitle: "Altered Carbon",
	}
	sess.SetSelectedPlaybackFile("Altered.Carbon.S02E03.1080p.mkv")
	sess.RecordUsedProviderHost("news-a.example.net")
	sess.RecordUsedProviderHost("news-b.example.net")
	sess.RecordServedProviderHost("news-b.example.net")

	params := server.recordAttemptParamsForOutcome(sess, true)
	if got := params.ServedFile; got != "Altered.Carbon.S02E03.1080p.mkv" {
		t.Fatalf("ServedFile = %q, want %q", got, "Altered.Carbon.S02E03.1080p.mkv")
	}
	if got := params.StreamName; got != "Stream04" {
		t.Fatalf("StreamName = %q, want %q", got, "Stream04")
	}
	if got := params.ContentTitle; got != "Altered Carbon" {
		t.Fatalf("ContentTitle = %q, want %q", got, "Altered Carbon")
	}
	if got := params.ProviderName; got != "news-b.example.net" {
		t.Fatalf("ProviderName = %q, want %q", got, "news-b.example.net")
	}
}

func TestRecordAttemptParamsFailureUsesUsedProviders(t *testing.T) {
	server := &Server{}
	sess := &session.Session{}
	sess.RecordUsedProviderHost("news-a.example.net")
	sess.RecordUsedProviderHost("news-b.example.net")
	sess.RecordServedProviderHost("news-b.example.net")

	params := server.recordAttemptParamsForOutcome(sess, false)
	if got := params.ProviderName; got != "news-a.example.net, news-b.example.net" {
		t.Fatalf("ProviderName = %q, want %q", got, "news-a.example.net, news-b.example.net")
	}
}

func TestRecordAttemptParamsFailureUsesAttemptedProvidersWhenNoUsedProvidersExist(t *testing.T) {
	server := &Server{}
	sess := &session.Session{}
	sess.RecordAttemptedProviderHost("news-a.example.net")
	sess.RecordAttemptedProviderHost("news-b.example.net")

	params := server.recordAttemptParamsForOutcome(sess, false)
	if got := params.ProviderName; got != "news-a.example.net, news-b.example.net" {
		t.Fatalf("ProviderName = %q, want %q", got, "news-a.example.net, news-b.example.net")
	}
}

func TestRecordAttemptParamsDerivesSeasonPackMatchType(t *testing.T) {
	server := &Server{}
	sess := &session.Session{
		ContentType: "series",
		ContentID:   "tt2261227:2:3",
	}
	sess.SetRelease(&release.Release{
		Title: "Altered.Carbon.S02.COMPLETE.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264.mkv",
	})

	params := server.recordAttemptParamsForOutcome(sess, true)
	if got := params.MatchType; got != "season_pack" {
		t.Fatalf("MatchType = %q, want %q", got, "season_pack")
	}
}

func TestRecordAttemptParamsDerivesCompletePackMatchType(t *testing.T) {
	server := &Server{}
	sess := &session.Session{
		ContentType: "series",
		ContentID:   "tt3032476:1:2",
	}
	sess.SetRelease(&release.Release{
		Title: "The.Good.Place.Complete.Series.1080p.NF.WEB-DL.DD5.1.x264-GROUP",
	})

	params := server.recordAttemptParamsForOutcome(sess, true)
	if got := params.MatchType; got != "complete_pack" {
		t.Fatalf("MatchType = %q, want %q", got, "complete_pack")
	}
}

func TestAllowLargestDirectFallbackForSessionMovie(t *testing.T) {
	sess := &session.Session{ContentType: "movie"}
	if !allowLargestDirectFallbackForSession(sess) {
		t.Fatal("expected movie sessions to allow largest direct fallback")
	}
}

func TestAllowLargestDirectFallbackForSessionExactEpisodeOnly(t *testing.T) {
	exact := &session.Session{
		ContentType: "series",
		ContentID:   "tt2261227:2:3",
	}
	exact.SetRelease(&release.Release{
		Title: "Altered.Carbon.S02E03.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264.mkv",
	})
	if !allowLargestDirectFallbackForSession(exact) {
		t.Fatal("expected exact episode sessions to allow largest direct fallback")
	}

	seasonPack := &session.Session{
		ContentType: "series",
		ContentID:   "tt2261227:2:3",
	}
	seasonPack.SetRelease(&release.Release{
		Title: "Altered.Carbon.S02.COMPLETE.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264.mkv",
	})
	if allowLargestDirectFallbackForSession(seasonPack) {
		t.Fatal("did not expect season pack sessions to allow largest direct fallback")
	}

	completePack := &session.Session{
		ContentType: "series",
		ContentID:   "tt3032476:1:2",
	}
	completePack.SetRelease(&release.Release{
		Title: "The.Good.Place.Complete.Series.1080p.NF.WEB-DL.DD5.1.x264-GROUP",
	})
	if allowLargestDirectFallbackForSession(completePack) {
		t.Fatal("did not expect complete pack sessions to allow largest direct fallback")
	}
}

func TestCacheReturnedPlaybackBlueprintReplacesStaleBlueprint(t *testing.T) {
	sess := &session.Session{}
	stale := &unpack.DirectBlueprint{FileName: "Show.S01E04.mkv", FileIndex: 1, Target: unpack.EpisodeTarget{Season: 1, Episode: 4}}
	fresh := &unpack.DirectBlueprint{FileName: "Show.S01E01.mkv", FileIndex: 0, Target: unpack.EpisodeTarget{Season: 1, Episode: 1}}
	sess.SetBlueprint(stale)

	playback.CacheReturnedBlueprint(sess, fresh)

	if got := sess.Blueprint(); got != fresh {
		t.Fatalf("expected session blueprint to be replaced, got %#v", got)
	}
}

func TestNormalizeAttemptReasonEOF(t *testing.T) {
	got := normalizeAttemptReason("EOF")
	want := "No playable media stream could be opened from this release (EOF)."
	if got != want {
		t.Fatalf("normalizeAttemptReason(EOF) = %q, want %q", got, want)
	}
}

func TestAvailOutcomeForFailureSegmentUnavailable(t *testing.T) {
	got := availOutcomeForFailure(errors.New("segment unavailable: fetch segment msgid: 430 No Such Article"))
	if got.Status != "skipped" {
		t.Fatalf("Status = %q, want skipped", got.Status)
	}
	want := "Not reported to AvailNZB because this segment fetch failure does not reliably prove the release is bad."
	if got.Reason != want {
		t.Fatalf("Reason = %q, want %q", got.Reason, want)
	}
}

func TestShouldReportBadReleaseAllProvidersThresholdExceeded(t *testing.T) {
	err := errors.Join(unpack.ErrTooManyZeroFills, errors.New("segment unavailable: 430 No Such Article"))
	if !shouldReportBadReleaseAllProviders(err) {
		t.Fatal("expected threshold-exceeded unavailable error to report across all providers")
	}
}

func TestShouldReportBadReleaseAllProvidersRegularUnavailable(t *testing.T) {
	err := errors.New("segment unavailable: fetch segment msgid: 430 No Such Article")
	if shouldReportBadReleaseAllProviders(err) {
		t.Fatal("expected single unavailable segment error not to report across all providers")
	}
}

func TestShouldReportBadReleaseAllProvidersFirstSegmentMissing(t *testing.T) {
	err := fmt.Errorf("segment unavailable: %w", ErrFirstSegmentUnavailable)
	if !shouldReportBadReleaseAllProviders(err) {
		t.Fatal("expected first-segment-430 startup failure to report across all providers")
	}
}

func TestCommitGoodAttemptIfQualifiedBelowThreshold(t *testing.T) {
	server := &Server{}
	sess := &session.Session{ID: "stream:test:movie:tmdb:1:0"}
	sess.AddBytesRead(32 << 20)

	if committed := server.commitGoodAttemptIfQualified(sess, sess.ID, time.Now().Add(-5*time.Second)); committed {
		t.Fatal("expected below-threshold attempt not to commit success")
	}
	if sess.OnceDone(onceSuccessRecorded) {
		t.Fatal("did not expect recorded success marker below threshold")
	}
}

func TestCommitGoodAttemptIfQualifiedCommitsAtThreshold(t *testing.T) {
	server := &Server{}

	// Bytes alone (fast connection, no sustained playback) must NOT commit —
	// this exact pattern produced a false good report 13s before a mid-file hole.
	bytesOnly := &session.Session{ID: "stream:test:movie:tmdb:2:0"}
	bytesOnly.AddBytesRead(65 << 20)
	if committed := server.commitGoodAttemptIfQualified(bytesOnly, bytesOnly.ID, time.Now()); committed {
		t.Fatal("bytes alone must not commit success")
	}

	// Duration alone (stalled stream) must NOT commit either.
	durationOnly := &session.Session{ID: "stream:test:movie:tmdb:3:0"}
	durationOnly.AddBytesRead(1 << 20)
	if committed := server.commitGoodAttemptIfQualified(durationOnly, durationOnly.ID, time.Now().Add(-21*time.Second)); committed {
		t.Fatal("duration alone must not commit success")
	}

	// Sustained playback (bytes AND duration) commits.
	sustained := &session.Session{ID: "stream:test:movie:tmdb:5:0"}
	sustained.AddBytesRead(65 << 20)
	if committed := server.commitGoodAttemptIfQualified(sustained, sustained.ID, time.Now().Add(-21*time.Second)); !committed {
		t.Fatal("expected sustained playback (bytes + duration) to commit success")
	}
	if !sustained.OnceDone(onceSuccessRecorded) {
		t.Fatal("expected recorded success marker after threshold commit")
	}
}

func TestCommitGoodAttemptIfQualifiedUsesAvailThresholds(t *testing.T) {
	server := &Server{}
	sess := &session.Session{ID: "stream:test:movie:tmdb:4:0"}
	sess.AddBytesRead(10 << 20)
	server.availReporter = &availnzb.Reporter{
		MinBytesToReportGood:    8 << 20,
		MinDurationToReportGood: 45 * time.Second,
		Disabled:                true,
	}

	// Bytes threshold met but duration not: must NOT commit (good requires both;
	// an early commit poisons AvailNZB for releases with holes past the startup window).
	if committed := server.commitGoodAttemptIfQualified(sess, sess.ID, time.Now()); committed {
		t.Fatal("bytes alone must not commit success before the duration threshold")
	}

	// Both custom thresholds met: commits.
	if committed := server.commitGoodAttemptIfQualified(sess, sess.ID, time.Now().Add(-time.Minute)); !committed {
		t.Fatal("expected custom thresholds (bytes + duration) to commit success")
	}
}

func TestPendingAttemptResolutionReason(t *testing.T) {
	got := pendingAttemptResolutionReason("Playback ended too early to classify this release as good.")
	want := "Playback probe ended before the good threshold was reached. Playback ended too early to classify this release as good."
	if got != want {
		t.Fatalf("pendingAttemptResolutionReason() = %q, want %q", got, want)
	}
}

// grabScoringServer is a bare server with just the registry scoreGrabOutcome
// writes into — no recorder, no session manager.
func grabScoringServer() *Server {
	return &Server{grabIndexerStats: make(map[string]GrabIndexerStats)}
}

func grabScoringSession(indexerName string, library bool) *session.Session {
	return grabScoringSessionUnique(indexerName, library, false)
}

func grabScoringSessionUnique(indexerName string, library, unique bool) *session.Session {
	sess := &session.Session{ID: "stream:global:movie:tt0111161:0:0:0"}
	sess.SetRelease(&release.Release{
		Title:      "The.Shawshank.Redemption.1994.2160p-GRP",
		DetailsURL: "https://geek.invalid/details/1",
		Indexer:    indexerName,
		IsLibrary:  library,
		UniqueHit:  unique,
	})
	return sess
}

func TestScoreGrabOutcomeCreditsAndDebitsTheSourceIndexer(t *testing.T) {
	server := grabScoringServer()
	server.scoreGrabOutcome(grabScoringSession("NZBGeek", false), true)
	server.scoreGrabOutcome(grabScoringSession("NZBGeek", false), false)
	server.scoreGrabOutcome(grabScoringSession("DrunkenSlug", false), false)

	stats := server.GetGrabIndexerStats()
	if got := stats["NZBGeek"]; got.Successful != 1 || got.Failed != 1 {
		t.Fatalf("NZBGeek = %+v, want 1 successful / 1 failed", got)
	}
	if got := stats["DrunkenSlug"]; got.Successful != 0 || got.Failed != 1 {
		t.Fatalf("DrunkenSlug = %+v, want 0 successful / 1 failed", got)
	}
}

// A player re-requesting the same slot, or a success followed by a mid-stream
// read error, must not score the indexer twice for one grab.
func TestScoreGrabOutcomeScoresEachSessionOnce(t *testing.T) {
	server := grabScoringServer()
	sess := grabScoringSession("NZBGeek", false)

	server.scoreGrabOutcome(sess, true)
	server.scoreGrabOutcome(sess, true)
	server.scoreGrabOutcome(sess, false)

	if got := server.GetGrabIndexerStats()["NZBGeek"]; got.Successful != 1 || got.Failed != 0 {
		t.Fatalf("NZBGeek = %+v, want 1 successful / 0 failed", got)
	}
}

// A library play reads NZB bytes off disk, so nothing was grabbed and the
// release carries a "StreamNZB Library - x" label rather than a real indexer.
func TestScoreGrabOutcomeIgnoresLibraryPlaysAndSessionsWithoutARelease(t *testing.T) {
	server := grabScoringServer()
	server.scoreGrabOutcome(grabScoringSession(libraryIndexerName("NZBGeek"), true), true)
	server.scoreGrabOutcome(&session.Session{ID: "stream:global:movie:tt0111161:0:0:0"}, false)
	server.scoreGrabOutcome(grabScoringSession("", false), true)

	if stats := server.GetGrabIndexerStats(); len(stats) != 0 {
		t.Fatalf("expected no grab stats, got %+v", stats)
	}
}

// The metric worth having is the overlap: a release that was exclusive to one
// indexer *and* played. Neither half counts on its own.
func TestScoreGrabOutcomeCountsUniqueSuccessesAsASubsetOfSuccesses(t *testing.T) {
	server := grabScoringServer()
	server.scoreGrabOutcome(grabScoringSessionUnique("NZBGeek", false, true), true)
	server.scoreGrabOutcome(grabScoringSessionUnique("NZBGeek", false, false), true)
	// Exclusive but unplayable: it earned a unique hit at search time and must
	// not also be credited as a unique play.
	server.scoreGrabOutcome(grabScoringSessionUnique("NZBGeek", false, true), false)

	got := server.GetGrabIndexerStats()["NZBGeek"]
	if got.Successful != 2 {
		t.Fatalf("Successful = %d, want 2", got.Successful)
	}
	if got.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", got.Failed)
	}
	if got.UniqueSuccessful != 1 {
		t.Fatalf("UniqueSuccessful = %d, want 1", got.UniqueSuccessful)
	}
}
