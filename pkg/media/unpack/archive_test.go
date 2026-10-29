package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestGetMediaStreamForEpisodeSkipsCachedDirectBlueprintForDifferentTarget(t *testing.T) {
	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E01.mkv", data: []byte("ep1")},
		&memoryUnpackableFile{name: "Show.S01E04.mkv", data: []byte("ep4")},
	}
	cachedBP := &DirectBlueprint{FileName: "Show.S01E04.mkv", FileIndex: 1, Target: EpisodeTarget{Season: 1, Episode: 4}}

	stream, name, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()

	if name != "Show.S01E01.mkv" {
		t.Fatalf("expected requested episode file, got %q", name)
	}
	if bp == cachedBP {
		t.Fatal("expected cached direct blueprint to be replaced")
	}
	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	if string(data) != "ep1" {
		t.Fatalf("expected episode 1 stream data, got %q", string(data))
	}
}

func TestGetMediaStreamForEpisodeSkipsCachedFailureForDifferentTarget(t *testing.T) {
	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E01.mkv", data: []byte("ep1")},
	}
	cachedBP := &FailedBlueprint{Err: io.EOF, Target: EpisodeTarget{Season: 1, Episode: 4}}

	stream, name, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()

	if name != "Show.S01E01.mkv" {
		t.Fatalf("expected requested episode file, got %q", name)
	}
	if bp == cachedBP {
		t.Fatal("expected cached failed blueprint to be replaced")
	}
}

func TestGetMediaStreamForEpisodeFailsWhenRequestedEpisodeMissingFromDirectFiles(t *testing.T) {
	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E04.mkv", data: []byte("ep4")},
		&memoryUnpackableFile{name: "Show.S01E06.mkv", data: []byte("ep6")},
	}

	stream, name, _, _, err := GetMediaStreamForEpisode(context.Background(), files, nil, "", EpisodeTarget{Season: 1, Episode: 1})
	if err == nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("expected missing-episode error")
	}
	if !errors.Is(err, ErrEpisodeTargetNotFound) {
		t.Fatalf("expected ErrEpisodeTargetNotFound, got %v", err)
	}
	if name != "" {
		t.Fatalf("expected no selected file, got %q", name)
	}
	if stream != nil {
		t.Fatal("expected no stream on missing episode")
	}
}

func TestGetMediaStreamRejectsSuspiciousLargestDirectFallback(t *testing.T) {
	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "release.par2", data: []byte("par2")},
			size:                 90 * 1024 * 1024,
		},
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "0)", data: []byte("bad")},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisode(context.Background(), files, nil, "", EpisodeTarget{})
	if err == nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("expected suspicious direct fallback to be rejected")
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if name != "" {
		t.Fatalf("expected no selected file, got %q", name)
	}
	if stream != nil {
		t.Fatal("expected no stream for suspicious direct fallback")
	}
}

func TestGetMediaStreamAllowsPlausibleLargestDirectFallback(t *testing.T) {
	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "abc12345", data: []byte("video")},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisode(context.Background(), files, nil, "", EpisodeTarget{})
	if err != nil {
		t.Fatalf("expected plausible direct fallback to succeed, got %v", err)
	}
	defer stream.Close()

	if name != "abc12345" {
		t.Fatalf("expected plausible fallback name, got %q", name)
	}
}

func TestGetMediaStreamForEpisodeWithHintsRefusesLargestDirectFallbackWhenDisabled(t *testing.T) {
	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "abc12345", data: []byte("video")},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisodeWithHints(
		context.Background(),
		files,
		nil,
		"",
		EpisodeTarget{},
		StreamSelectionHints{AllowLargestDirectFallback: false},
	)
	if err == nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("expected disabled largest direct fallback to fail")
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if name != "" {
		t.Fatalf("expected no selected file, got %q", name)
	}
	if stream != nil {
		t.Fatal("expected no stream when fallback is disabled")
	}
}

func TestGetMediaStreamForEpisodeAllowsLargestDirectFallbackWhenHinted(t *testing.T) {
	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "abc12345", data: []byte("video")},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisodeWithHints(
		context.Background(),
		files,
		nil,
		"",
		EpisodeTarget{Season: 1, Episode: 1},
		StreamSelectionHints{AllowLargestDirectFallback: true},
	)
	if err != nil {
		t.Fatalf("expected hinted targeted fallback to succeed, got %v", err)
	}
	defer stream.Close()

	if name != "abc12345" {
		t.Fatalf("expected plausible fallback name, got %q", name)
	}
}

func TestGetMediaStreamForEpisodeNamesUnknownFileFromItsSignature(t *testing.T) {
	// An extension-less file whose first bytes are an EBML header: the
	// signature scan names it, so it is selected as direct media rather than
	// having to reach the content probe.
	data := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 1024)...)
	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "abc12345", data: data},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisodeWithHints(
		context.Background(),
		files,
		nil,
		"",
		EpisodeTarget{},
		StreamSelectionHints{AllowLargestDirectFallback: false},
	)
	if err != nil {
		t.Fatalf("expected signature-named candidate to succeed, got %v", err)
	}
	defer stream.Close()

	if name != "abc12345.mkv" {
		t.Fatalf("expected the recovered container extension on the original stem, got %q", name)
	}
}

func TestGetMediaStreamForEpisodeSelectsUnknownNameByContentProbe(t *testing.T) {
	// The EBML header sits past offset 0, so the signature scan cannot name the
	// file and selection has to fall through to the content probe.
	data := append(make([]byte, 64), []byte{0x1A, 0x45, 0xDF, 0xA3}...)
	data = append(data, make([]byte, 1024)...)
	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "abc12345", data: data},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisodeWithHints(
		context.Background(),
		files,
		nil,
		"",
		EpisodeTarget{},
		StreamSelectionHints{AllowLargestDirectFallback: false},
	)
	if err != nil {
		t.Fatalf("expected content probe fallback to succeed, got %v", err)
	}
	defer stream.Close()

	if name != "abc12345" {
		t.Fatalf("expected probed candidate name, got %q", name)
	}
}

func TestPlausibleLargestDirectFallbackAllowsCommonReleasePunctuation(t *testing.T) {
	if !isPlausibleLargestDirectFallbackName("Movie, Title '11 [1080p]") {
		t.Fatal("expected common release punctuation to remain plausible")
	}
}

// A compressed archive is a permanent property of the release, so the fast-probe
// caveat — which exists for scans that may simply not have looked hard enough —
// must not be stamped onto it. Marking it makes the caller fail open and the
// release comes back on every search forever.
func TestMaybeMarkArchiveFastProbeLeavesDefinitiveVerdictsUnmarked(t *testing.T) {
	compressed := fmt.Errorf("compressed RAR archive (file: %s): %w", "obfuscated.mkv", ErrCompressedArchive)

	got := maybeMarkArchiveFastProbe(context.Background(), compressed)
	if errors.Is(got, ErrArchiveFastProbe) {
		t.Fatalf("compressed-archive verdict was softened into a fast-probe result: %v", got)
	}
	if !errors.Is(got, ErrCompressedArchive) {
		t.Fatalf("expected ErrCompressedArchive to survive, got %v", got)
	}
}

func TestMaybeMarkArchiveFastProbeStillMarksInconclusiveScanFailures(t *testing.T) {
	got := maybeMarkArchiveFastProbe(context.Background(), errors.New("RAR header scan failed on 2 volume(s)"))
	if !errors.Is(got, ErrArchiveFastProbe) {
		t.Fatalf("expected ErrArchiveFastProbe on an inconclusive scan failure, got %v", got)
	}
}

func TestMaybeMarkArchiveFastProbePassesNilThrough(t *testing.T) {
	if err := maybeMarkArchiveFastProbe(context.Background(), nil); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// warmableStubFile records what a tail warm asks of a volume.
type warmableStubFile struct {
	stubFile
	ensured    int
	prefetches [][2]int64
	err        error
}

func (f *warmableStubFile) EnsureSegmentMapCtx(context.Context) error {
	f.ensured++
	return f.err
}

func (f *warmableStubFile) PrefetchPlaybackRange(_ context.Context, offset, length int64) {
	f.prefetches = append(f.prefetches, [2]int64{offset, length})
}

func TestWarmPlaybackTailMapsAndPullsTheVolumeTheMediaEndsIn(t *testing.T) {
	head := &warmableStubFile{stubFile: stubFile{name: "x.part01.rar", size: 1000}}
	tail := &warmableStubFile{stubFile: stubFile{name: "x.part02.rar", size: 64 << 20}}
	bp := &ArchiveBlueprint{
		MainFileName: "movie.mkv",
		Parts: []VirtualPartDef{
			{VirtualStart: 0, VirtualEnd: 999, VolFile: head},
			{VirtualStart: 1000, VirtualEnd: 1999, VolFile: tail},
		},
	}
	if !WarmPlaybackTail(context.Background(), bp, nil) {
		t.Fatal("expected the tail volume to be warmed")
	}
	if tail.ensured != 1 {
		t.Fatalf("tail volume mapped %d times, want 1", tail.ensured)
	}
	if len(tail.prefetches) != 1 {
		t.Fatalf("tail volume prefetched %d ranges, want 1", len(tail.prefetches))
	}
	if got := tail.prefetches[0]; got[0] != tail.Size()-playbackTailWarmBytes || got[1] != playbackTailWarmBytes {
		t.Fatalf("prefetched %v, want the last %d bytes", got, int64(playbackTailWarmBytes))
	}
	// The head is already mapped and being read by startup; warming it again
	// would spend articles the opening bytes need.
	if head.ensured != 0 || len(head.prefetches) != 0 {
		t.Fatalf("head volume was warmed: ensured=%d prefetches=%d", head.ensured, len(head.prefetches))
	}
}

// A directly-posted release has no volumes to map — its cost is the cold
// articles at the end of the one file, which is what the player reads first.
func TestWarmPlaybackTailPullsTheEndOfADirectFile(t *testing.T) {
	other := &warmableStubFile{stubFile: stubFile{name: "sample.mkv", size: 1 << 20}}
	media := &warmableStubFile{stubFile: stubFile{name: "movie.mkv", size: 32 << 20}}
	bp := &DirectBlueprint{FileName: "movie.mkv", FileIndex: 1}

	if !WarmPlaybackTail(context.Background(), bp, []UnpackableFile{other, media}) {
		t.Fatal("expected the direct file to be warmed")
	}
	if len(media.prefetches) != 1 {
		t.Fatalf("media prefetched %d ranges, want 1", len(media.prefetches))
	}
	if got := media.prefetches[0]; got[0] != media.Size()-playbackTailWarmBytes {
		t.Fatalf("prefetched from %d, want the last %d bytes", got[0], int64(playbackTailWarmBytes))
	}
	if len(other.prefetches) != 0 {
		t.Fatal("warmed a file the blueprint does not play")
	}
}

// The recorded index belongs to the file list the blueprint was built from; a
// replay rebuilds that list, so the name has to win.
func TestWarmPlaybackTailResolvesDirectFileByName(t *testing.T) {
	media := &warmableStubFile{stubFile: stubFile{name: "movie.mkv", size: 32 << 20}}
	decoy := &warmableStubFile{stubFile: stubFile{name: "extras.mkv", size: 32 << 20}}
	bp := &DirectBlueprint{FileName: "movie.mkv", FileIndex: 1}

	if !WarmPlaybackTail(context.Background(), bp, []UnpackableFile{media, decoy}) {
		t.Fatal("expected the named file to be warmed")
	}
	if len(media.prefetches) != 1 || len(decoy.prefetches) != 0 {
		t.Fatalf("warm followed the stale index: media=%d decoy=%d", len(media.prefetches), len(decoy.prefetches))
	}
}

func TestWarmPlaybackTailIsBestEffort(t *testing.T) {
	failing := &warmableStubFile{stubFile: stubFile{name: "x.part01.rar"}, err: errors.New("no article")}
	bp := &ArchiveBlueprint{Parts: []VirtualPartDef{{VolFile: failing}}}
	if WarmPlaybackTail(context.Background(), bp, nil) {
		t.Fatal("a failed warm must report itself, not claim success")
	}
	if len(failing.prefetches) != 0 {
		t.Fatal("prefetched a volume whose map could not be built")
	}
	if WarmPlaybackTail(context.Background(), &ArchiveBlueprint{}, nil) {
		t.Fatal("a blueprint with no parts has no tail to warm")
	}
	if WarmPlaybackTail(context.Background(), &DirectBlueprint{FileName: "gone.mkv"}, nil) {
		t.Fatal("a direct blueprint with no matching file has nothing to warm")
	}
	if WarmPlaybackTail(context.Background(), &FailedBlueprint{}, nil) {
		t.Fatal("a failed blueprint has nothing to warm")
	}
}

// #382 added the air date to the target. A plan saved without it (every
// blueprint persisted before the change) is reused unless an air date could
// have steered its pick: a dated pick, a pick numbered for another episode, or
// a failure. A plan saved with a date that has since changed or gone missing
// is never reused: the date may have narrowed its files at the NZB layer.
func TestBlueprintTargetMatchesAcrossAirDateChanges(t *testing.T) {
	const day, nextDay = "2026-10-26", "2026-10-27"
	s1e5 := EpisodeTarget{Season: 1, Episode: 5}
	withDate := func(target EpisodeTarget, date string) EpisodeTarget { target.AirDate = date; return target }
	direct := func(name string, target EpisodeTarget) Blueprint {
		return &DirectBlueprint{FileName: name, Target: target}
	}

	for _, tc := range []struct {
		name      string
		cached    Blueprint
		requested EpisodeTarget
		want      bool
	}{
		{"identical targets", direct("Show.S01E05.mkv", withDate(s1e5, day)), withDate(s1e5, day), true},
		{"identical failure", &FailedBlueprint{Target: withDate(s1e5, day)}, withDate(s1e5, day), true},
		{"legacy numbered pick, date now known", direct("Show.S01E05.mkv", s1e5), withDate(s1e5, day), true},
		{"legacy rar numbered pick, date now known", &ArchiveBlueprint{MainFileName: "show.s01e05.720p.mkv", Target: s1e5}, withDate(s1e5, day), true},
		{"legacy 7z numbered pick, date now known", &SevenZipBlueprint{MainFileName: "Show.S01E05.mkv", Target: s1e5}, withDate(s1e5, day), true},
		{"numbered pick, date changed", direct("Show.S01E05.mkv", withDate(s1e5, day)), withDate(s1e5, nextDay), false},
		{"numbered pick, date gone missing", direct("Show.S01E05.mkv", withDate(s1e5, day)), s1e5, false},
		{"legacy dated pick, date now known", direct("Show.2026.10.26.mkv", s1e5), withDate(s1e5, day), false},
		{"dated pick, date changed", direct("Show.2026.10.26.mkv", withDate(s1e5, day)), withDate(s1e5, nextDay), false},
		{"dated pick, date gone missing", direct("Show.2026.10.26.mkv", withDate(s1e5, day)), s1e5, false},
		{"numbered and dated pick", direct("Show.S01E05.2026.10.26.mkv", s1e5), withDate(s1e5, day), false},
		{"multi-episode pick", direct("Show.S01E05E06.mkv", s1e5), withDate(s1e5, day), true},
		{"season pack file pick", direct("Show.S01.mkv", s1e5), withDate(s1e5, day), true},
		{"title-only pick", direct("Show.Name.1080p.WEB.h264-GROUP.mkv", s1e5), withDate(s1e5, day), true},
		{"another episode's pick", direct("Show.S01E06.mkv", s1e5), withDate(s1e5, day), false},
		{"obfuscated pick", direct("cc3299e0692fb5e3a8737f1c00361e72.mkv", s1e5), withDate(s1e5, day), true},
		{"obfuscated pick, date gone missing", direct("cc3299e0692fb5e3a8737f1c00361e72.mkv", withDate(s1e5, day)), s1e5, false},
		{"obfuscated pick, date changed", &ArchiveBlueprint{MainFileName: "a8f3e2c1b7d94e06a8f3e2c1b7d94e06.mkv", Target: withDate(s1e5, day)}, withDate(s1e5, nextDay), false},
		{"legacy rar obfuscated pick", &ArchiveBlueprint{MainFileName: "a8f3e2c1b7d94e06a8f3e2c1b7d94e06.mkv", Target: s1e5}, withDate(s1e5, day), true},
		{"legacy failure, date now known", &FailedBlueprint{Target: s1e5}, withDate(s1e5, day), false},
		{"different episode", direct("Show.S01E05.mkv", s1e5), withDate(EpisodeTarget{Season: 1, Episode: 6}, day), false},
	} {
		if got := blueprintTargetMatches(tc.cached, tc.requested); got != tc.want {
			t.Errorf("%s: reuse = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// End to end: a legacy plan whose file names the episode is replayed without
// a rescan once the air date is known.
func TestGetMediaStreamForEpisodeReusesLegacyNumberedBlueprint(t *testing.T) {
	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E05.mkv", data: []byte("ep5")},
		&memoryUnpackableFile{name: "Show.S01E06.mkv", data: []byte("ep6")},
	}
	cachedBP := &DirectBlueprint{FileName: "Show.S01E05.mkv", FileIndex: 0, Target: EpisodeTarget{Season: 1, Episode: 5}}

	stream, name, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 1, Episode: 5, AirDate: "2026-10-26"})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()
	if name != "Show.S01E05.mkv" || bp != cachedBP {
		t.Fatalf("got %q (reused=%v), want the cached plan replayed", name, bp == cachedBP)
	}
}

// End to end: a legacy plan in a release holding several nights, pointing at
// the wrong one, is not trusted once the air date is known — the rescan finds
// the requested night.
func TestGetMediaStreamForEpisodeRescansLegacyDatedBlueprint(t *testing.T) {
	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.2026.10.25.mkv", data: []byte("mon")},
		&memoryUnpackableFile{name: "Show.2026.10.26.mkv", data: []byte("tue")},
	}
	cachedBP := &DirectBlueprint{FileName: "Show.2026.10.25.mkv", FileIndex: 0, Target: EpisodeTarget{Season: 24, Episode: 2524}}

	stream, name, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 24, Episode: 2524, AirDate: "2026-10-26"})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()
	data, _ := io.ReadAll(stream)
	if name != "Show.2026.10.26.mkv" || string(data) != "tue" || bp == cachedBP {
		t.Fatalf("got %q %q (reused=%v), want a rescan selecting the 2026-10-26 file", name, data, bp == cachedBP)
	}
	if got := bp.TargetEpisode().AirDate; got != "2026-10-26" {
		t.Fatalf("new plan's air date = %q, want 2026-10-26 so the next play reuses it", got)
	}
}

// The review's case: a library item's plan saved before #382, whose pick is
// obfuscated. Library-sourced sessions never re-save their plan, so a rescan
// here would repeat on every replay; an air date cannot change a pick without
// a date in its name, so the plan is replayed.
func TestGetMediaStreamForEpisodeReusesLegacyObfuscatedBlueprint(t *testing.T) {
	files := []UnpackableFile{
		&memoryUnpackableFile{name: "cc3299e0692fb5e3a8737f1c00361e72.mkv", data: []byte("ep5")},
	}
	cachedBP := &DirectBlueprint{FileName: "cc3299e0692fb5e3a8737f1c00361e72.mkv", FileIndex: 0, Target: EpisodeTarget{Season: 1, Episode: 5}}

	stream, _, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 1, Episode: 5, AirDate: "2026-10-26"})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()
	if bp != cachedBP {
		t.Fatal("expected the legacy obfuscated plan to be replayed, not rescanned")
	}
}

// The review's case: a plan built with an air date, for a multi-night release
// whose NZB was narrowed to that night's dated volumes around an obfuscated
// inner file. When the date changes, the plan's file name cannot show the
// date chose it, so the plan is rebuilt rather than replayed.
func TestGetMediaStreamForEpisodeRescansBlueprintWhenAirDateChanges(t *testing.T) {
	files := []UnpackableFile{
		&memoryUnpackableFile{name: "cc3299e0692fb5e3a8737f1c00361e72.mkv", data: []byte("tue")},
	}
	cachedBP := &DirectBlueprint{FileName: "cc3299e0692fb5e3a8737f1c00361e72.mkv", FileIndex: 0, Target: EpisodeTarget{Season: 24, Episode: 2524, AirDate: "2026-10-25"}}

	stream, _, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 24, Episode: 2524, AirDate: "2026-10-26"})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()
	if bp == cachedBP {
		t.Fatal("expected a plan made for another air date to be rebuilt, not replayed")
	}
	if got := bp.TargetEpisode().AirDate; got != "2026-10-26" {
		t.Fatalf("new plan's air date = %q, want 2026-10-26", got)
	}
}
