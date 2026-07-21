package stremio

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"streamnzb/pkg/media/unpack"
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

	params := server.recordAttemptParamsForFailure(sess)
	if got := params.ProviderName; got != "news-a.example.net, news-b.example.net" {
		t.Fatalf("ProviderName = %q, want %q", got, "news-a.example.net, news-b.example.net")
	}
}

func TestRecordAttemptParamsDerivesSeasonPackMatchType(t *testing.T) {
	server := &Server{}
	sess := &session.Session{
		ContentType: "series",
		ContentID:   "tt2261227:2:3",
		Release: &release.Release{
			Title: "Altered.Carbon.S02.COMPLETE.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264.mkv",
		},
	}

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
		Release: &release.Release{
			Title: "The.Good.Place.Complete.Series.1080p.NF.WEB-DL.DD5.1.x264-GROUP",
		},
	}

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
		Release: &release.Release{
			Title: "Altered.Carbon.S02E03.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264.mkv",
		},
	}
	if !allowLargestDirectFallbackForSession(exact) {
		t.Fatal("expected exact episode sessions to allow largest direct fallback")
	}

	seasonPack := &session.Session{
		ContentType: "series",
		ContentID:   "tt2261227:2:3",
		Release: &release.Release{
			Title: "Altered.Carbon.S02.COMPLETE.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264.mkv",
		},
	}
	if allowLargestDirectFallbackForSession(seasonPack) {
		t.Fatal("did not expect season pack sessions to allow largest direct fallback")
	}

	completePack := &session.Session{
		ContentType: "series",
		ContentID:   "tt3032476:1:2",
		Release: &release.Release{
			Title: "The.Good.Place.Complete.Series.1080p.NF.WEB-DL.DD5.1.x264-GROUP",
		},
	}
	if allowLargestDirectFallbackForSession(completePack) {
		t.Fatal("did not expect complete pack sessions to allow largest direct fallback")
	}
}

func TestCacheReturnedPlaybackBlueprintReplacesStaleBlueprint(t *testing.T) {
	sess := &session.Session{}
	stale := &unpack.DirectBlueprint{FileName: "Show.S01E04.mkv", FileIndex: 1, Target: unpack.EpisodeTarget{Season: 1, Episode: 4}}
	fresh := &unpack.DirectBlueprint{FileName: "Show.S01E01.mkv", FileIndex: 0, Target: unpack.EpisodeTarget{Season: 1, Episode: 1}}
	sess.SetBlueprint(stale)

	cacheReturnedPlaybackBlueprint(sess, fresh)

	if got := sess.Blueprint; got != fresh {
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

	if committed := server.commitGoodAttemptIfQualified(sess, sess.ID, sess.ID, time.Now().Add(-5*time.Second)); committed {
		t.Fatal("expected below-threshold attempt not to commit success")
	}
	if _, ok := server.recordedSuccessSessionIDs.Load(sess.ID); ok {
		t.Fatal("did not expect recorded success marker below threshold")
	}
}

func TestCommitGoodAttemptIfQualifiedCommitsAtThreshold(t *testing.T) {
	server := &Server{}
	sess := &session.Session{ID: "stream:test:movie:tmdb:2:0"}
	sess.AddBytesRead(65 << 20)

	if committed := server.commitGoodAttemptIfQualified(sess, sess.ID, sess.ID, time.Now()); !committed {
		t.Fatal("expected threshold-reaching attempt to commit success immediately")
	}
	if _, ok := server.recordedSuccessSessionIDs.Load(sess.ID); !ok {
		t.Fatal("expected recorded success marker after threshold commit")
	}
}

func TestCommitGoodAttemptIfQualifiedCommitsAtDurationThreshold(t *testing.T) {
	server := &Server{}
	sess := &session.Session{ID: "stream:test:movie:tmdb:3:0"}
	sess.AddBytesRead(1 << 20)

	if committed := server.commitGoodAttemptIfQualified(sess, sess.ID, sess.ID, time.Now().Add(-21*time.Second)); !committed {
		t.Fatal("expected duration threshold to commit success")
	}
	if _, ok := server.recordedSuccessSessionIDs.Load(sess.ID); !ok {
		t.Fatal("expected recorded success marker after duration threshold commit")
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

	if committed := server.commitGoodAttemptIfQualified(sess, sess.ID, sess.ID, time.Now()); !committed {
		t.Fatal("expected custom threshold to commit success")
	}
}

func TestPendingAttemptResolutionReason(t *testing.T) {
	got := pendingAttemptResolutionReason("Playback ended too early to classify this release as good.")
	want := "Playback probe ended before the good threshold was reached. Playback ended too early to classify this release as good."
	if got != want {
		t.Fatalf("pendingAttemptResolutionReason() = %q, want %q", got, want)
	}
}
