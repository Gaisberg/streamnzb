package ffprobe

import (
	"encoding/json"
	"testing"
)

func TestFFprobeOutputParsesFormatDuration(t *testing.T) {
	var out FFprobeOutput
	if err := json.Unmarshal([]byte(`{"streams":[],"format":{"duration":"6900.032000"}}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Format.Duration != "6900.032000" {
		t.Errorf("Format.Duration = %q, want %q", out.Format.Duration, "6900.032000")
	}
}

func TestFFprobeDownloadURL(t *testing.T) {
	url, targetName, err := FFprobeDownloadURL()
	if err != nil {
		t.Fatalf("expected valid download URL, got error: %v", err)
	}
	if url == "" {
		t.Fatal("expected non-empty download URL")
	}
	if targetName == "" {
		t.Fatal("expected non-empty target name")
	}
}

func TestFindFFprobeBinaryCustomPath(t *testing.T) {
	// A missing custom path must not silently resolve to whatever ffprobe
	// happens to be on PATH -- which many developer machines have (Homebrew,
	// apt, ...) even though CI does not, which is why this only failed there.
	t.Setenv("PATH", "")
	_, ok := FindFFprobeBinary("non_existent_binary_path_xyz")
	if ok {
		t.Fatal("expected false for non-existent binary path")
	}
}

func TestNeedsLegacyShowEntries(t *testing.T) {
	if !needsLegacyShowEntries("No match for section 'stream_side_data'") {
		t.Fatal("expected old ffprobe capability error to select the compatibility query")
	}
	if needsLegacyShowEntries("Invalid data found when processing input") {
		t.Fatal("a media error must not be retried as an old-ffprobe capability error")
	}
}

func TestFFprobeDownloadURLForAllTargets(t *testing.T) {
	targets := []struct{ goos, goarch string }{
		{"windows", "amd64"},
		{"linux", "amd64"},
		{"linux", "arm64"},
		{"darwin", "amd64"},
		{"darwin", "arm64"},
	}
	for _, tt := range targets {
		url, name, err := FFprobeDownloadURLFor(tt.goos, tt.goarch)
		if err != nil {
			t.Fatalf("%s/%s: unexpected error: %v", tt.goos, tt.goarch, err)
		}
		if url == "" || name == "" {
			t.Fatalf("%s/%s: empty url or name", tt.goos, tt.goarch)
		}
	}
	if _, _, err := FFprobeDownloadURLFor("plan9", "amd64"); err == nil {
		t.Fatal("expected error for unsupported OS")
	}
}

func TestSummarizeStreamsFirstQualifyingVideoWins(t *testing.T) {
	// A real HEVC 4K video followed by an mjpeg cover art still. The result must
	// report the HEVC track, not the artwork's codec/dimensions.
	streams := []FFprobeStream{
		{CodecType: "video", CodecName: "hevc", Profile: "Main 10", Width: 3840, Height: 2160,
			PixFmt: "yuv420p10le", ColorTransfer: "smpte2084", CodecTagString: "hev1", NbReadFrames: "120"},
		{CodecType: "audio", CodecName: "eac3"},
		{CodecType: "video", CodecName: "mjpeg", Width: 600, Height: 600,
			Disposition: ffprobeDisposition{AttachedPic: 1}, NbReadFrames: "1"},
	}
	res := summarizeStreams(streams)
	if !res.HasVideo || res.VideoCodec != "hevc" {
		t.Fatalf("expected hevc video, got has_video=%v codec=%q", res.HasVideo, res.VideoCodec)
	}
	if res.Width != 3840 || res.Height != 2160 {
		t.Fatalf("expected 3840x2160, got %dx%d (cover art overwrote real video)", res.Width, res.Height)
	}
	if res.BitDepth != 10 {
		t.Fatalf("expected 10-bit, got %d", res.BitDepth)
	}
	if res.HDR != "HDR10" {
		t.Fatalf("expected HDR10, got %q", res.HDR)
	}
	if res.AudioCodec != "eac3" {
		t.Fatalf("expected eac3 audio, got %q", res.AudioCodec)
	}
}

func TestSummarizeStreamsTrackLanguages(t *testing.T) {
	// A dual-audio anime episode as muxers actually tag it: ISO 639-2 codes,
	// a second untagged audio track, and a subtitle track with a region.
	streams := []FFprobeStream{
		{CodecType: "video", CodecName: "hevc", Width: 1920, Height: 1080, NbReadFrames: "120"},
		{CodecType: "audio", CodecName: "aac", Tags: ffprobeTags{Language: "jpn"}},
		{CodecType: "audio", CodecName: "eac3", Tags: ffprobeTags{Language: "eng"}},
		{CodecType: "audio", CodecName: "aac"},
		{CodecType: "subtitle", CodecName: "ass", Tags: ffprobeTags{Language: "eng"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: ffprobeTags{Language: "pt-BR"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: ffprobeTags{Language: "und"}},
	}
	res := summarizeStreams(streams)
	if got, want := res.AudioLanguages, []string{"ja", "en"}; !equalStrings(got, want) {
		t.Fatalf("audio languages = %v, want %v", got, want)
	}
	if res.AudioStreams != 3 {
		t.Fatalf("audio streams = %d, want 3 (untagged track still counts)", res.AudioStreams)
	}
	if got, want := res.SubtitleLanguages, []string{"en", "pt"}; !equalStrings(got, want) {
		t.Fatalf("subtitle languages = %v, want %v", got, want)
	}
	if res.SubtitleStreams != 3 {
		t.Fatalf("subtitle streams = %d, want 3", res.SubtitleStreams)
	}
	if res.AudioCodec != "aac" {
		t.Fatalf("first audio codec = %q, want aac", res.AudioCodec)
	}
}

func TestNormalizeLanguageTag(t *testing.T) {
	for tag, want := range map[string]string{
		"jpn": "ja", "eng": "en", "ara": "ar", "ger": "de", "deu": "de",
		"JA": "ja", "en_US": "en", "pt-BR": "pt", "und": "", "": "", "mul": "",
		"xyz": "", "chi": "zh", "zho": "zh", "afr": "af", "bel": "be", "bos": "bs",
		"cym": "cy", "wel": "cy", "gle": "ga", "swa": "sw", "fil": "tl",
		// jhin's vocabulary, not bare ISO: one Norwegian, and no Latin because
		// "la" is Latino downstream.
		"nob": "no", "nno": "no", "nor": "no", "nb": "no", "nn": "no", "lat": "", "la": "",
		"scr": "hr", "scc": "sr",
	} {
		if got := NormalizeLanguageTag(tag); got != want {
			t.Errorf("NormalizeLanguageTag(%q) = %q, want %q", tag, got, want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSummarizeStreamsCoverArtIsNotVideo(t *testing.T) {
	// Audio file with embedded cover art: attached_pic video + audio. Must be
	// treated as audio-only so the audio-only guard fires.
	streams := []FFprobeStream{
		{CodecType: "video", CodecName: "png", Disposition: ffprobeDisposition{AttachedPic: 1}, NbReadFrames: "1"},
		{CodecType: "audio", CodecName: "flac"},
	}
	res := summarizeStreams(streams)
	if res.HasVideo {
		t.Fatal("cover art must not count as a video stream")
	}
	if !res.HasAudio || res.AudioCodec != "flac" {
		t.Fatalf("expected flac audio, got has_audio=%v codec=%q", res.HasAudio, res.AudioCodec)
	}
}

// TestSummarizeStreamsVideoQualification pins down which video streams count.
//
// This used to assert the opposite for the zero-frame case — that a forced
// decode yielding 0 frames meant the track was not really playable. It does
// not. Probing one BluRay remux both ways showed the same file, same reported
// duration, reporting hevc video on the header pass and no video at all on the
// forced-decode pass; the only difference between the runs is -count_frames.
// High-bitrate remuxes routinely complete no frame inside the read window
// while the WEB-DLs beside them manage a few dozen, so the rule rejected
// exactly the best releases — silently, because preloading probes with
// StrictFFprobe and just moves to the next candidate.
//
// Zero frames is therefore inconclusive, not disqualifying. Exactly one frame
// still is: that is what a still image decodes to, which is the whole point of
// the test. Article holes are unaffected — the probe still reads its full
// -probesize of payload, and a 430 surfaces as a stream error, which outranks
// ffprobe's exit code upstream of here.
func TestSummarizeStreamsVideoQualification(t *testing.T) {
	video := func(codec, frames string, attachedPic int) FFprobeStream {
		return FFprobeStream{
			CodecType:    "video",
			CodecName:    codec,
			Width:        3840,
			Height:       2160,
			NbReadFrames: frames,
			Disposition:  ffprobeDisposition{AttachedPic: attachedPic},
		}
	}
	for _, tc := range []struct {
		name     string
		stream   FFprobeStream
		wantReal bool
	}{
		{"remux completed no frame in the window", video("hevc", "0", 0), true},
		{"web-dl completed a handful", video("hevc", "19", 0), true},
		{"frames not counted at all", video("hevc", "N/A", 0), true},
		{"frames field absent", video("hevc", "", 0), true},
		{"exactly one frame is a still image", video("hevc", "1", 0), false},
		{"attached picture", video("hevc", "240", 1), false},
		{"still image codec", video("mjpeg", "240", 0), false},
	} {
		res := summarizeStreams([]FFprobeStream{tc.stream})
		if res.HasVideo != tc.wantReal {
			t.Errorf("%s: HasVideo = %v, want %v", tc.name, res.HasVideo, tc.wantReal)
		}
		if tc.wantReal && (res.Width != 3840 || res.Height != 2160) {
			t.Errorf("%s: dimensions dropped: %dx%d", tc.name, res.Width, res.Height)
		}
	}

	// The shape of the file that was being rejected: a video track the probe
	// could not finish a frame of, three TrueHD tracks and dozens of
	// subtitles. It must not read as audio-only.
	res := summarizeStreams([]FFprobeStream{
		video("hevc", "0", 0),
		{CodecType: "audio", CodecName: "truehd"},
		{CodecType: "audio", CodecName: "eac3"},
		{CodecType: "subtitle", CodecName: "subrip"},
	})
	if !res.HasVideo || res.VideoCodec != "hevc" {
		t.Fatalf("remux read as audio-only: %+v", res)
	}
	if res.AudioStreams != 2 || res.SubtitleStreams != 1 {
		t.Fatalf("companion tracks miscounted: %+v", res)
	}
}

func TestIsStillImageCodec(t *testing.T) {
	for _, c := range []string{"mjpeg", "png", "PNG", "webp"} {
		if !isStillImageCodec(c) {
			t.Errorf("expected %q to be a still-image codec", c)
		}
	}
	for _, c := range []string{"hevc", "h264", "av1", "vp9"} {
		if isStillImageCodec(c) {
			t.Errorf("expected %q not to be a still-image codec", c)
		}
	}
}

func TestBitDepthFromStream(t *testing.T) {
	cases := []struct {
		st   FFprobeStream
		want int
	}{
		{FFprobeStream{BitsPerRawSample: "10", PixFmt: "yuv420p"}, 10},
		{FFprobeStream{PixFmt: "yuv420p10le"}, 10},
		{FFprobeStream{PixFmt: "yuv420p12le"}, 12},
		{FFprobeStream{PixFmt: "yuv420p"}, 8},
		{FFprobeStream{PixFmt: "p010le"}, 10},
	}
	for _, c := range cases {
		if got := bitDepthFromStream(c.st); got != c.want {
			t.Errorf("bitDepthFromStream(%+v) = %d, want %d", c.st, got, c.want)
		}
	}
}

func TestClassifyHDRAndDolbyVision(t *testing.T) {
	if got := classifyHDR(FFprobeStream{ColorTransfer: "smpte2084"}); got != "HDR10" {
		t.Errorf("smpte2084 => %q, want HDR10", got)
	}
	if got := classifyHDR(FFprobeStream{ColorTransfer: "arib-std-b67"}); got != "HLG" {
		t.Errorf("arib-std-b67 => %q, want HLG", got)
	}
	if got := classifyHDR(FFprobeStream{ColorTransfer: "bt709"}); got != "" {
		t.Errorf("bt709 => %q, want empty", got)
	}
	if !isDolbyVision(FFprobeStream{CodecTagString: "dvh1"}) {
		t.Error("dvh1 tag should be detected as Dolby Vision")
	}
	if isDolbyVision(FFprobeStream{CodecTagString: "hev1"}) {
		t.Error("hev1 tag should not be Dolby Vision")
	}
}

func TestParseIntOK(t *testing.T) {
	if _, ok := parseIntOK("N/A"); ok {
		t.Error("N/A should be treated as absent")
	}
	if _, ok := parseIntOK(""); ok {
		t.Error("empty should be treated as absent")
	}
	if n, ok := parseIntOK("120"); !ok || n != 120 {
		t.Errorf("parseIntOK(120) = %d,%v", n, ok)
	}
}
