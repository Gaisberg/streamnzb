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
	_, ok := FindFFprobeBinary("non_existent_binary_path_xyz")
	if ok {
		t.Fatal("expected false for non-existent binary path")
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

func TestSummarizeStreamsUndecodableVideoRejected(t *testing.T) {
	// Forced decode yielded 0 frames -> the "video" stream is not really playable.
	streams := []FFprobeStream{
		{CodecType: "video", CodecName: "hevc", Width: 3840, Height: 2160, NbReadFrames: "0"},
	}
	res := summarizeStreams(streams)
	if res.HasVideo {
		t.Fatal("a video stream that decoded 0 frames must not count as playable")
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
