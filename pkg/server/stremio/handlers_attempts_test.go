package stremio

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// Season 0 is the Specials season, not "no season": an S00E01 release is an
// exact match for tt…:0:1, and a seasonless (unmapped Kitsu) session does not
// get read as season 0.
func TestRecordAttemptParamsDerivesSpecialsMatchType(t *testing.T) {
	server := &Server{}
	for _, sess := range []*session.Session{
		{ContentType: "series", ContentID: "tt0903747:0:1"},
		{ContentType: "series", ContentID: "tt0903747:0:1", ContentIDs: &session.AvailReportMeta{Season: 0, Episode: 1}},
	} {
		sess.SetRelease(&release.Release{
			Title: "Breaking.Bad.S00E01.Good.Cop.Bad.Cop.1080p.WEB-DL.mkv",
		})
		params := server.recordAttemptParamsForOutcome(sess, true)
		if got := params.MatchType; got != "exact_episode" {
			t.Fatalf("MatchType = %q, want %q", got, "exact_episode")
		}
	}

	seasonless := &session.Session{
		ContentType: "series",
		ContentID:   "kitsu:1:1",
		ContentIDs:  &session.AvailReportMeta{Seasonless: true, Episode: 1},
	}
	seasonless.SetRelease(&release.Release{
		Title: "Breaking.Bad.S00E01.Good.Cop.Bad.Cop.1080p.WEB-DL.mkv",
	})
	if got := server.recordAttemptParamsForOutcome(seasonless, true).MatchType; got != "" {
		t.Fatalf("MatchType = %q, want no match type for a seasonless session", got)
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

// rawSession is a WWE Raw S33E43 session — aired 2025-10-27 — playing the
// release given, with the air date search validation matched it by.
func rawSession(title, airDate string) *session.Session {
	sess := &session.Session{
		ContentType: "series",
		ContentID:   "tt0185103:33:43",
		ContentIDs:  &session.AvailReportMeta{ImdbID: "tt0185103", TvdbID: "76779", Season: 33, Episode: 43, AirDate: airDate},
	}
	sess.SetRelease(&release.Release{Title: title})
	return sess
}

// A release search validation accepted by its air date alone is the exact
// episode at playback too (#382), and nothing else changes: the date never
// rescues a conflicting number, packs and numbered releases classify as
// before, and anime keeps classifying by season/episode only.
func TestMatchTypeForSessionCountsTheAirDate(t *testing.T) {
	const day = "2025-10-27"
	for _, tc := range []struct {
		name string
		sess *session.Session
		want string
	}{
		{"date-only, matching", rawSession("WWE.Raw.2025.10.27.1080p.WEB.h264-GROUP", day), "exact_episode"},
		{"date-only, another night", rawSession("WWE.Raw.2026.10.26.1080p.WEB.h264-GROUP", day), ""},
		{"date-only, no air date known", rawSession("WWE.Raw.2025.10.27.1080p.WEB.h264-GROUP", ""), ""},
		{"conflicting number beside the date", rawSession("WWE.Raw.S33E44.2025.10.27.1080p.WEB.h264-GROUP", day), ""},
		{"numbered", rawSession("WWE.Raw.S33E43.1080p.WEB.h264-GROUP", day), "exact_episode"},
		{"multi-episode", rawSession("WWE.Raw.S33E43E44.1080p.WEB.h264-GROUP", day), "multi_episode"},
		{"season pack", rawSession("WWE.Raw.S33.1080p.WEB.h264-GROUP", day), "season_pack"},
		{"complete pack", rawSession("WWE.Raw.Complete.Series.1080p.WEB.h264-GROUP", day), "complete_pack"},
	} {
		if got := matchTypeForSession(tc.sess, "series"); got != tc.want {
			t.Errorf("%s: match type = %q, want %q", tc.name, got, tc.want)
		}
	}

	anime := &session.Session{
		ContentType: "series",
		ContentID:   "kitsu:12:1100",
		ContentIDs:  &session.AvailReportMeta{Season: 21, Episode: 7, AbsoluteEpisode: 1100, AirDate: "2024-03-10"},
	}
	anime.SetRelease(&release.Release{Title: "[Grp] One Piece - 1100 [1080p].mkv"})
	if got := matchTypeForSession(anime, "series"); got != "" {
		t.Errorf("absolute-numbered anime: match type = %q, want \"\" as before", got)
	}
}

// obfuscatedFile is an extensionless, hash-named media file of the size the
// largest-file fallback considers, read back as zeros.
type obfuscatedFile struct{ name string }

const obfuscatedFileSize = 64 << 20

func (f obfuscatedFile) Name() string            { return f.name }
func (f obfuscatedFile) Size() int64             { return obfuscatedFileSize }
func (f obfuscatedFile) EnsureSegmentMap() error { return nil }
func (f obfuscatedFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= obfuscatedFileSize {
		return 0, io.EOF
	}
	n := min(int64(len(p)), obfuscatedFileSize-off)
	clear(p[:n])
	if n < int64(len(p)) {
		return int(n), io.EOF
	}
	return int(n), nil
}
func (f obfuscatedFile) OpenStream() (io.ReadSeekCloser, error) {
	return f.OpenStreamCtx(context.Background())
}
func (f obfuscatedFile) OpenStreamCtx(context.Context) (io.ReadSeekCloser, error) {
	return struct {
		*io.SectionReader
		io.Closer
	}{io.NewSectionReader(f, 0, obfuscatedFileSize), io.NopCloser(nil)}, nil
}
func (f obfuscatedFile) OpenReaderAt(_ context.Context, offset int64) (io.ReadCloser, error) {
	return io.NopCloser(io.NewSectionReader(f, offset, obfuscatedFileSize-offset)), nil
}

// The review's scenario end to end: a release accepted by its air date whose
// only content is an extensionless obfuscated file. Nothing names the episode
// inside it, so playback rests on the largest-file fallback — open to the
// matching night's release, and still refused to another night's.
func TestDailyReleaseWithObfuscatedContentUsesLargestFileFallback(t *testing.T) {
	files := []unpack.UnpackableFile{obfuscatedFile{name: "9f2c1a7b3e5d4c60a8b1"}}
	target := unpack.EpisodeTarget{Season: 33, Episode: 43, AirDate: "2025-10-27"}
	open := func(sess *session.Session) (string, error) {
		hints := unpack.StreamSelectionHints{AllowLargestDirectFallback: allowLargestDirectFallbackForSession(sess)}
		stream, name, _, _, err := unpack.GetMediaStreamForEpisodeWithHints(context.Background(), files, nil, "", target, hints)
		if stream != nil {
			stream.Close()
		}
		return name, err
	}

	if name, err := open(rawSession("WWE.Raw.2025.10.27.1080p.WEB.h264-GROUP", "2025-10-27")); err != nil || name != "9f2c1a7b3e5d4c60a8b1" {
		t.Fatalf("matching night: got %q, %v; want the obfuscated file streamed", name, err)
	}
	if _, err := open(rawSession("WWE.Raw.2026.10.26.1080p.WEB.h264-GROUP", "2025-10-27")); !errors.Is(err, unpack.ErrEpisodeTargetNotFound) {
		t.Fatalf("another night: err = %v, want ErrEpisodeTargetNotFound (fallback refused)", err)
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

// The skip reason is also what the library and the bad-release log show next
// to a durable verdict, so it must say what was found and must not read as
// "this proves nothing" (#350).
func TestAvailOutcomeForFailureSegmentUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "430 on every provider",
			err:  errors.New("segment unavailable: fetch segment msgid: 430 No Such Article"),
			want: "Articles are missing on every provider you use (430 No Such Article). Not reported to AvailNZB because providers on other backbones may still carry them.",
		},
		{
			name: "article missing from the NZB",
			err:  errors.New("segment unavailable: article missing from NZB (segment 7)"),
			want: "This NZB has no article for part of the release. Not reported to AvailNZB because another NZB of the same release may be complete.",
		},
	}
	for _, tc := range cases {
		got := availOutcomeForFailure(tc.err)
		if got.Status != "skipped" {
			t.Fatalf("%s: Status = %q, want skipped", tc.name, got.Status)
		}
		if got.Reason != tc.want {
			t.Fatalf("%s: Reason = %q, want %q", tc.name, got.Reason, tc.want)
		}
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

	if committed := server.commitGoodAttemptIfQualified(nil, sess, sess.ID, time.Now().Add(-5*time.Second), nil); committed {
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
	if committed := server.commitGoodAttemptIfQualified(nil, bytesOnly, bytesOnly.ID, time.Now(), nil); committed {
		t.Fatal("bytes alone must not commit success")
	}

	// Duration alone (stalled stream) must NOT commit either.
	durationOnly := &session.Session{ID: "stream:test:movie:tmdb:3:0"}
	durationOnly.AddBytesRead(1 << 20)
	if committed := server.commitGoodAttemptIfQualified(nil, durationOnly, durationOnly.ID, time.Now().Add(-21*time.Second), nil); committed {
		t.Fatal("duration alone must not commit success")
	}

	// Sustained playback (bytes AND duration) commits.
	sustained := &session.Session{ID: "stream:test:movie:tmdb:5:0"}
	sustained.AddBytesRead(65 << 20)
	if committed := server.commitGoodAttemptIfQualified(nil, sustained, sustained.ID, time.Now().Add(-21*time.Second), nil); !committed {
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
	if committed := server.commitGoodAttemptIfQualified(nil, sess, sess.ID, time.Now(), nil); committed {
		t.Fatal("bytes alone must not commit success before the duration threshold")
	}

	// Both custom thresholds met: commits.
	if committed := server.commitGoodAttemptIfQualified(nil, sess, sess.ID, time.Now().Add(-time.Minute), nil); !committed {
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
