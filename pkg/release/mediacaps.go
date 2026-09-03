package release

import (
	"fmt"
	"strings"
)

// MediaCaps holds client-relevant properties of a media file as measured by
// ffprobe. It lets the addon distinguish "this release is broken" from "this
// client cannot decode this codec."
//
// It lives here, in a leaf package, rather than next to the playback session
// that first captures it: the same values are persisted on library items and
// read back during search ranking, and pkg/search cannot import pkg/session
// without pointing an import sideways. pkg/session keeps an alias so playback
// code reads unchanged.
type MediaCaps struct {
	VideoCodec    string
	AudioCodec    string
	Width         int
	Height        int
	Profile       string
	PixFmt        string
	BitDepth      int
	HDR           string // "", "HDR10", "HDR10+", "HLG"
	DolbyVision   bool
	ColorTransfer string
	CodecTag      string
	// DurationSeconds is the container-reported duration, 0 when the probe
	// could not see one (and on library items saved before it was captured).
	DurationSeconds float64
	// AudioLanguages and SubtitleLanguages are the tagged track languages as
	// ISO 639-1 codes in stream order; AudioStreams and SubtitleStreams count
	// every track, tagged or not. Nil on library items saved before they
	// were captured.
	AudioLanguages    []string
	SubtitleLanguages []string
	AudioStreams      int
	SubtitleStreams   int
}

// Summary renders a short human-readable capability string suitable for a
// Stremio stream description, e.g. "hevc Main 10 2160p 10-bit DV + HDR10".
//
// Dolby Vision and the HDR base layer are reported together because they are
// independent: a DV release carrying an HDR10 fallback plays on a device with
// no DV support, and one without it does not. Collapsing them to "Dolby
// Vision" hid exactly the distinction that decides whether a release is
// watchable.
func (c *MediaCaps) Summary() string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, 6)
	if c.VideoCodec != "" {
		parts = append(parts, c.VideoCodec)
	}
	if c.Profile != "" {
		parts = append(parts, c.Profile)
	}
	if c.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dp", c.Height))
	}
	if c.BitDepth > 0 {
		parts = append(parts, fmt.Sprintf("%d-bit", c.BitDepth))
	}
	if dr := c.DynamicRange(); dr != "" {
		parts = append(parts, dr)
	}
	if c.AudioCodec != "" {
		parts = append(parts, c.AudioCodec)
	}
	return strings.Join(parts, " ")
}

// DynamicRange names the release's HDR handling: "DV + HDR10" when Dolby
// Vision ships over a base layer a non-DV device can fall back to, "DV only"
// when it does not, the base format's own name when there is no Dolby Vision,
// and "" for SDR.
func (c *MediaCaps) DynamicRange() string {
	if c == nil {
		return ""
	}
	if c.DolbyVision {
		if c.HDR != "" {
			return "DV + " + c.HDR
		}
		return "DV only"
	}
	return c.HDR
}

// HasHDRFallback reports whether a device with no Dolby Vision support still
// gets an HDR picture. It is false for SDR and for DV-only releases alike, so
// it answers "will this look right without DV" rather than "is this HDR".
func (c *MediaCaps) HasHDRFallback() bool {
	return c != nil && c.HDR != ""
}
