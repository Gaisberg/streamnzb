package ffprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/core/logger"
)

// DefaultDecodeFrames is the number of frames forced through the decoder when
// ProbeOptions.ForceDecode is enabled. ~120 frames of a 4K HEVC stream pulls
// roughly 10-20MB of real payload, which is enough to trip over article holes
// (e.g. 430 No Such Article) that a header-only probe structurally cannot see.
const DefaultDecodeFrames = 120

// ffprobeDisposition captures the stream disposition flags we care about.
type ffprobeDisposition struct {
	AttachedPic int `json:"attached_pic"`
}

// ffprobeTags is the per-stream tag block. language is the only tag read: the
// ISO 639-2 code a muxer wrote on an audio or subtitle track.
type ffprobeTags struct {
	Language string `json:"language"`
}

type FFprobeStream struct {
	CodecType        string             `json:"codec_type"`
	CodecName        string             `json:"codec_name"`
	Profile          string             `json:"profile"`
	Width            int                `json:"width,omitempty"`
	Height           int                `json:"height,omitempty"`
	PixFmt           string             `json:"pix_fmt"`
	ColorTransfer    string             `json:"color_transfer"`
	ColorPrimaries   string             `json:"color_primaries"`
	CodecTagString   string             `json:"codec_tag_string"`
	BitRate          string             `json:"bit_rate"`
	BitsPerRawSample string             `json:"bits_per_raw_sample"`
	NbReadFrames     string             `json:"nb_read_frames"`
	Disposition      ffprobeDisposition `json:"disposition"`
	Tags             ffprobeTags        `json:"tags"`
	// SideDataList carries the DOVI configuration record. Dolby Vision
	// profile 8 rides on an ordinary hvc1/hev1 stream with an HDR10 base
	// layer, so the codec tag says nothing and this is the only place the
	// profile shows up at all.
	SideDataList []ffprobeSideData `json:"side_data_list"`
}

// ffprobeSideData is one stream side-data entry. Only the Dolby Vision
// configuration record is read; the rest are ignored.
type ffprobeSideData struct {
	SideDataType string `json:"side_data_type"`
	DVProfile    *int   `json:"dv_profile"`
	DVLevel      *int   `json:"dv_level"`
}

// FFprobeFormat carries the container-level fields we ask for. Duration comes
// from the container header (MKV Segment Info, MP4 moov), so it is usually
// known even on a piped probe that never reads the whole file — and absent
// when the header does not state one (e.g. a moov-at-end MP4 on a pipe).
type FFprobeFormat struct {
	Duration string `json:"duration"`
}

type FFprobeOutput struct {
	Streams []FFprobeStream `json:"streams"`
	Format  FFprobeFormat   `json:"format"`
}

// FFprobeResult summarizes the probed media. The capability fields (Profile,
// PixFmt, HDR, ...) are captured from the first *qualifying* video stream so
// callers can distinguish "the file is broken" from "this client can't decode
// this codec."
type FFprobeResult struct {
	HasVideo   bool
	HasAudio   bool
	VideoCodec string
	AudioCodec string
	Width      int
	Height     int

	// Capability metadata, from the first real (non cover-art) video stream.
	Profile        string
	PixFmt         string
	ColorTransfer  string
	ColorPrimaries string
	CodecTag       string
	BitDepth       int
	HDR            string // "", "HDR10", "HDR10+", "HLG"
	DolbyVision    bool
	FramesDecoded  int // nb_read_frames of the chosen video stream (only when ForceDecode)

	// DurationSeconds is the container-reported duration, 0 when the header
	// does not state one.
	DurationSeconds float64

	// Track languages, as ISO 639-1 codes in stream order, deduplicated. A
	// track with no language tag (or "und") contributes nothing, so a file
	// can have AudioStreams == 2 and one entry in AudioLanguages. This is the
	// measured counterpart of what a release name claims: the name says
	// "DUAL", the tracks say ["ja", "en"].
	AudioLanguages    []string
	SubtitleLanguages []string
	// AudioStreams and SubtitleStreams count the tracks whether or not they
	// were tagged, so "dual audio" is answerable even from an untagged file.
	AudioStreams    int
	SubtitleStreams int
}

// ProbeOptions tunes how aggressively ProbeStream inspects the input.
type ProbeOptions struct {
	// ForceDecode adds -count_frames + -read_intervals so ffprobe actually pulls
	// and decodes packets instead of stopping at the container header. This is the
	// difference between validating a Matroska Tracks element (<1MB) and reading
	// several MB of real payload.
	ForceDecode bool
	// DecodeFrames overrides the number of frames to force-decode. 0 => DefaultDecodeFrames.
	DecodeFrames int
	// QuickHeader caps -probesize/-analyzeduration far below the thorough
	// defaults. On a network-backed stream the default 50M probesize can pull
	// tens of MB before the caller gets an answer — over a second of wall
	// clock that lands directly on time-to-first-byte when the probe sits on
	// the serve path. Track headers live in the first few MB of every
	// container we serve; anything the small window misses degrades to the
	// caller's permissive fallback, not to a rejection.
	QuickHeader bool
}

func FindFFprobeBinary(customPath string) (string, bool) {
	if strings.TrimSpace(customPath) != "" {
		if _, err := os.Stat(customPath); err == nil {
			return customPath, true
		}
	}

	execDir := ""
	if ex, err := os.Executable(); err == nil {
		execDir = filepath.Dir(ex)
	}

	candidates := []string{}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, "ffprobe.exe", filepath.Join(execDir, "ffprobe.exe"))
	} else {
		candidates = append(candidates, "ffprobe", filepath.Join(execDir, "ffprobe"))
	}

	for _, cand := range candidates {
		if _, err := os.Stat(cand); err == nil {
			if abs, err := filepath.Abs(cand); err == nil {
				return abs, true
			}
			return cand, true
		}
	}

	if path, err := exec.LookPath("ffprobe"); err == nil {
		return path, true
	}

	if path, ok := ExtractEmbeddedBinary(); ok {
		return path, true
	}

	return "", false
}

// showEntries is the -show_entries spec: enough stream fields to both validate
// playability and capture client-relevant capabilities (profile, bit depth, HDR).
const showEntries = "stream=codec_type,codec_name,profile,width,height,pix_fmt," +
	"color_transfer,color_primaries,codec_tag_string,bit_rate,bits_per_raw_sample,nb_read_frames:" +
	"stream_disposition=attached_pic:" +
	"stream_tags=language:" +
	"stream_side_data=side_data_type,dv_profile,dv_level:" +
	"format=duration"

// legacyShowEntries deliberately omits stream_side_data. FFmpeg 4.4, including
// the ARM64 static binary this service installs, rejects that section before it
// reads the input at all. We retain the richer query for newer ffprobe builds
// and only retry with this one after that exact capability error.
const legacyShowEntries = "stream=codec_type,codec_name,profile,width,height,pix_fmt," +
	"color_transfer,color_primaries,codec_tag_string,bit_rate,bits_per_raw_sample,nb_read_frames:" +
	"stream_disposition=attached_pic:" +
	"stream_tags=language:" +
	"format=duration"

func needsLegacyShowEntries(stderr string) bool {
	return strings.Contains(stderr, "No match for section 'stream_side_data'")
}

// rewindProbeStream returns the stream to the start so a second ffprobe run
// reads the container header rather than resuming wherever the first run's
// stdin copy left off. Reports false when the stream cannot be rewound.
func rewindProbeStream(stream io.Reader) bool {
	seeker, ok := stream.(io.Seeker)
	if !ok {
		return false
	}
	_, err := seeker.Seek(0, io.SeekStart)
	return err == nil
}

// ProbeStream runs a lightweight, header-only inspection (backwards-compatible).
func ProbeStream(ctx context.Context, stream io.Reader, customPath string) (*FFprobeResult, error) {
	return ProbeStreamWithOptions(ctx, stream, customPath, ProbeOptions{})
}

// ProbeStreamWithOptions runs ffprobe against the reader, optionally forcing a
// real multi-frame decode.
func ProbeStreamWithOptions(ctx context.Context, stream io.Reader, customPath string, opts ProbeOptions) (*FFprobeResult, error) {
	binaryPath, ok := FindFFprobeBinary(customPath)
	if !ok {
		return nil, errors.New("ffprobe binary not found in PATH or working directory")
	}

	timeout := 15 * time.Second
	if opts.ForceDecode {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// -analyzeduration is microseconds of content, -probesize is bytes.
	probesize, analyzeduration := "50M", "20M"
	if opts.QuickHeader {
		probesize, analyzeduration = "5M", "5M"
	}
	// A seekable stream is served over a loopback range server instead of a
	// pipe. On a pipe ffprobe cannot seek, so an MP4/MOV whose moov sits at the
	// tail (anything not written with faststart) is structurally unprobeable,
	// and even a happy probe streams the full -probesize window through stdin.
	// Over HTTP with Accept-Ranges, ffprobe seeks to exactly the boxes it
	// needs. A stream that cannot seek keeps the pipe path.
	var srv *probeStreamServer
	// QuickHeader stays on the pipe deliberately: that probe sits on
	// time-to-first-byte and is bounded BY being unseekable — over a seekable
	// input ffprobe wanders to tail cues through cold segments, and its
	// open-new-connection-before-closing-old seek pattern spends multi-second
	// stalls the quick budget cannot afford. The thorough paths keep the
	// loopback server for what it buys: a moov-at-end MP4 is only probeable
	// with seeks.
	if seeker, ok := stream.(io.ReadSeeker); ok && !opts.QuickHeader {
		var srvErr error
		srv, srvErr = newProbeStreamServer(seeker)
		if srvErr != nil {
			logger.Debug("FFprobe loopback server unavailable, probing over a pipe", "err", srvErr)
			srv = nil
		}
	}
	if srv != nil {
		defer srv.Close()
	}

	run := func(entries string) (string, string, error, error) {
		args := []string{
			"-v", "error",
			"-probesize", probesize,
			"-analyzeduration", analyzeduration,
			"-show_entries", entries,
			"-of", "json",
		}
		if opts.ForceDecode {
			frames := opts.DecodeFrames
			if frames <= 0 {
				frames = DefaultDecodeFrames
			}
			args = append(args, "-count_frames", "-read_intervals", fmt.Sprintf("%%+#%d", frames))
		}
		if srv != nil {
			args = append(args, srv.URL())
		} else {
			args = append(args, "pipe:0")
		}

		cmd := exec.CommandContext(ctx, binaryPath, args...)
		var rr *recordingReader
		if srv == nil {
			rr = &recordingReader{r: stream}
			cmd.Stdin = rr
		}
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			var streamErr error
			if rr != nil {
				streamErr = rr.lastErr
			} else if srv != nil {
				streamErr = srv.LastErr()
			}
			return stdout.String(), stderr.String(), err, streamErr
		}
		return stdout.String(), stderr.String(), nil, nil
	}

	stdout, stderr, runErr, streamErr := run(showEntries)
	if runErr != nil && streamErr == nil && needsLegacyShowEntries(stderr) {
		// The first attempt died in ffprobe's option parsing without reading a
		// byte — but on the pipe path exec has already started a goroutine
		// copying the stream into the child's stdin, and it drains a pipe
		// buffer's worth (64 KiB on Linux) before noticing the process is
		// gone. Those bytes are consumed from the reader for good.
		//
		// Retrying without rewinding therefore hands ffprobe a stream that
		// starts mid-container. It finds no header and no duration, reports
		// whatever elementary stream it stumbles into, and exits 0 — which the
		// validation layer above reads as a definitive "audio-only, missing
		// video track" verdict against a perfectly good remux, and records as
		// a two-week bad-release blacklisting.
		//
		// The loopback server path needs nothing: each run makes its own range
		// requests and the handler seeks per request. Rewinding the shared
		// seeker from here would race those handlers instead.
		switch {
		case srv != nil:
			logger.Debug("FFprobe lacks stream_side_data support; retrying compatibility query", "binary", binaryPath)
			stdout, stderr, runErr, streamErr = run(legacyShowEntries)
		case rewindProbeStream(stream):
			logger.Debug("FFprobe lacks stream_side_data support; retrying compatibility query", "binary", binaryPath)
			stdout, stderr, runErr, streamErr = run(legacyShowEntries)
		default:
			// Probing a partially drained stream would answer confidently
			// about the wrong bytes, which is worse than not answering.
			logger.Debug("FFprobe lacks stream_side_data support but the stream cannot be rewound; not retrying", "binary", binaryPath)
		}
	}
	if runErr != nil {
		// Same error recovery as the pipe path: the stream's own failure
		// outranks ffprobe's opaque non-zero exit.
		if streamErr != nil {
			return nil, fmt.Errorf("ffprobe execution failed (%v): %s: %w", runErr, strings.TrimSpace(stderr), streamErr)
		}
		return nil, fmt.Errorf("ffprobe execution failed (%v): %s", runErr, strings.TrimSpace(stderr))
	}

	var output FFprobeOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		return nil, fmt.Errorf("parse ffprobe json output: %w", err)
	}

	res := summarizeStreams(output.Streams)
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(output.Format.Duration), 64); err == nil && seconds > 0 {
		res.DurationSeconds = seconds
	}

	logger.Debug("FFprobe stream inspection completed",
		"binary", binaryPath,
		"force_decode", opts.ForceDecode,
		"has_video", res.HasVideo,
		"video_codec", res.VideoCodec,
		"profile", res.Profile,
		"width", res.Width,
		"height", res.Height,
		"pix_fmt", res.PixFmt,
		"bit_depth", res.BitDepth,
		"hdr", res.HDR,
		"dolby_vision", res.DolbyVision,
		"codec_tag", res.CodecTag,
		"frames_decoded", res.FramesDecoded,
		"has_audio", res.HasAudio,
		"audio_codec", res.AudioCodec,
		"audio_streams", res.AudioStreams,
		"audio_languages", res.AudioLanguages,
		"subtitle_streams", res.SubtitleStreams,
		"subtitle_languages", res.SubtitleLanguages,
		"duration_s", res.DurationSeconds,
	)

	return res, nil
}

// summarizeStreams reduces ffprobe's stream list to a single result, choosing
// the FIRST qualifying video stream (so real video followed by cover art does
// not get its codec/dimensions overwritten by the artwork).
func summarizeStreams(streams []FFprobeStream) *FFprobeResult {
	res := &FFprobeResult{}
	videoChosen := false
	for _, st := range streams {
		switch st.CodecType {
		case "video":
			if videoChosen || !isRealVideoStream(st) {
				continue // cover art / still image / undecodable — not a real video track
			}
			res.HasVideo = true
			res.VideoCodec = st.CodecName
			res.Width = st.Width
			res.Height = st.Height
			res.Profile = st.Profile
			res.PixFmt = st.PixFmt
			res.ColorTransfer = st.ColorTransfer
			res.ColorPrimaries = st.ColorPrimaries
			res.CodecTag = st.CodecTagString
			res.BitDepth = bitDepthFromStream(st)
			res.HDR = classifyHDR(st)
			res.DolbyVision = isDolbyVision(st)
			if n, ok := parseIntOK(st.NbReadFrames); ok {
				res.FramesDecoded = n
			}
			videoChosen = true
		case "audio":
			res.HasAudio = true
			res.AudioStreams++
			if res.AudioCodec == "" {
				res.AudioCodec = st.CodecName
			}
			res.AudioLanguages = appendLanguage(res.AudioLanguages, st.Tags.Language)
		case "subtitle":
			res.SubtitleStreams++
			res.SubtitleLanguages = appendLanguage(res.SubtitleLanguages, st.Tags.Language)
		}
	}
	return res
}

// appendLanguage adds a track's language tag to the list as an ISO 639-1 code,
// skipping untagged and undetermined tracks and codes already present.
func appendLanguage(list []string, tag string) []string {
	code := NormalizeLanguageTag(tag)
	if code == "" {
		return list
	}
	for _, have := range list {
		if have == code {
			return list
		}
	}
	return append(list, code)
}

// isRealVideoStream rejects "video" streams that are actually embedded cover art
// (attached_pic disposition, a still-image codec, or a stream that decoded <=1
// frame when a forced decode was requested).
func isRealVideoStream(st FFprobeStream) bool {
	if st.Disposition.AttachedPic == 1 {
		return false
	}
	if isStillImageCodec(st.CodecName) {
		return false
	}
	// Exactly one decoded frame is cover art: a still image is one frame, which
	// is the whole point of the test.
	//
	// Zero is a different thing entirely — the decoder produced nothing inside
	// the sampled interval — and a bounded probe window does that to any
	// high-bitrate track. A 2160p HEVC remux frame is megabytes, so -probesize
	// runs out before one completes, while the 1080p h264 file next to it in
	// the same playlist manages a couple of dozen. Counting that as artwork
	// reported whole Dolby Vision remuxes as audio-only, and because
	// preloading probes with StrictFFprobe a false reject silently moved on to
	// the next candidate: the best releases were the ones being discarded.
	//
	// nb_read_frames is only a number when -count_frames was passed, so this
	// test applies to the forced-decode paths alone; elsewhere it reads "N/A"
	// and is skipped. A track that genuinely decodes nothing still shows up as
	// a stream error, which outranks ffprobe's exit code upstream of here.
	if n, ok := parseIntOK(st.NbReadFrames); ok && n == 1 {
		return false
	}
	return true
}

// isStillImageCodec reports whether codec is a still-image codec typically used
// for embedded artwork rather than a real video track.
func isStillImageCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "mjpeg", "png", "bmp", "gif", "tiff", "webp", "jpeg", "jpegls", "ppm", "pgm", "pam", "targa", "tga":
		return true
	default:
		return false
	}
}

// bitDepthFromStream derives the luma bit depth, preferring the explicit
// bits_per_raw_sample and falling back to the pixel format naming convention.
func bitDepthFromStream(st FFprobeStream) int {
	if n, ok := parseIntOK(st.BitsPerRawSample); ok && n > 0 {
		return n
	}
	pf := strings.ToLower(st.PixFmt)
	switch {
	case strings.Contains(pf, "12le"), strings.Contains(pf, "12be"), strings.Contains(pf, "p012"):
		return 12
	case strings.Contains(pf, "10le"), strings.Contains(pf, "10be"), strings.Contains(pf, "p010"):
		return 10
	case pf == "":
		return 0
	default:
		return 8
	}
}

// classifyHDR maps the transfer characteristics to a coarse HDR bucket.
func classifyHDR(st FFprobeStream) string {
	switch strings.ToLower(strings.TrimSpace(st.ColorTransfer)) {
	case "smpte2084", "smptest2084":
		return "HDR10"
	case "arib-std-b67":
		return "HLG"
	default:
		return ""
	}
}

// isDolbyVision detects Dolby Vision.
//
// Three tiers, because DV hides in a different place per profile. Profile 5
// announces itself in the codec tag (dvhe/dvh1), which most players cannot
// decode. Profile 8 — what a "DV HDR" or "DV HDR10Plus" WEB-DL almost always
// is — rides on an ordinary hvc1/hev1 HEVC stream with an HDR10 base layer:
// the tag is unremarkable and the only evidence is the DOVI configuration
// record in the stream's side data. Reading the tag alone reports such a file
// as plain HDR10, which is how a release named DV reaches a viewer who asked
// for no DV.
func isDolbyVision(st FFprobeStream) bool {
	switch strings.ToLower(strings.TrimSpace(st.CodecTagString)) {
	case "dvhe", "dvh1", "dva1", "dav1", "dvav":
		return true
	}
	if strings.Contains(strings.ToLower(st.Profile), "dolby vision") {
		return true
	}
	for _, side := range st.SideDataList {
		if strings.Contains(strings.ToLower(side.SideDataType), "dovi") ||
			strings.Contains(strings.ToLower(side.SideDataType), "dolby vision") {
			return true
		}
		if side.DVProfile != nil && *side.DVProfile > 0 {
			return true
		}
	}
	return false
}

// recordingReader wraps the probe input and remembers the last non-EOF read
// error so the underlying cause survives ffprobe's non-zero exit. exec's stdin
// copy goroutine finishes before cmd.Run returns, so lastErr is safe to read after.
type recordingReader struct {
	r       io.Reader
	lastErr error
}

func (rr *recordingReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	if err != nil && err != io.EOF {
		rr.lastErr = err
	}
	return n, err
}

// parseIntOK parses an ffprobe integer field, treating "N/A"/"" as absent.
func parseIntOK(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "N/A") {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
