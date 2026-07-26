package stremio

import (
	"fmt"
	"strings"

	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/triage"
)

func buildStreamMetadata(url, filename string, cand triage.Candidate, sizeGB float64, totalBytes int64, rel *release.Release) Stream {
	meta := cand.Metadata

	name := buildStreamName(meta, cand.Group)
	description := buildDetailedDescription(meta, sizeGB, filename)
	hints := &BehaviorHints{
		NotWebReady: false,
		BingeGroup:  fmt.Sprintf("streamnzb|%s", cand.Group),
		VideoSize:   totalBytes,
		Filename:    filename,
	}

	return Stream{
		URL:            url,
		Name:           name,
		Description:    description,
		BehaviorHints:  hints,
		StreamType:     "usenet",
		Score:          cand.Score,
		ParsedMetadata: cand.Metadata,
		Release:        rel,
	}
}

func buildStreamName(meta *parser.ParsedRelease, group string) string {
	parts := []string{}

	parts = append(parts, strings.ToUpper(group))

	if meta.Quality != "" {

		quality := meta.Quality
		quality = strings.ReplaceAll(quality, "Blu-ray", "BluRay")
		quality = strings.ReplaceAll(quality, "WEB-DL", "WEB")
		parts = append(parts, quality)
	}

	return strings.Join(parts, " ")
}

func buildDetailedDescription(meta *parser.ParsedRelease, sizeGB float64, filename string) string {
	lines := []string{}

	line1 := []string{}
	if meta.Quality != "" {
		line1 = append(line1, fmt.Sprintf("📡 %s", meta.Quality))
	}
	if meta.Codec != "" {
		codec := strings.ToUpper(meta.Codec)
		codec = strings.ReplaceAll(codec, "H.265", "HEVC")
		codec = strings.ReplaceAll(codec, "H.264", "AVC")
		codec = strings.ReplaceAll(codec, "X265", "HEVC")
		codec = strings.ReplaceAll(codec, "X264", "AVC")
		line1 = append(line1, fmt.Sprintf("🎞️ %s", codec))
	}
	if meta.Container != "" {
		line1 = append(line1, fmt.Sprintf("📦 %s", strings.ToUpper(meta.Container)))
	}
	if len(line1) > 0 {
		lines = append(lines, strings.Join(line1, " "))
	}

	line2 := []string{}
	visualTags := make([]string, 0)
	visualTags = append(visualTags, meta.HDR...)
	if meta.ThreeD {
		visualTags = append(visualTags, "3D")
	}
	if len(visualTags) > 0 {
		tags := strings.Join(visualTags, "|")
		line2 = append(line2, fmt.Sprintf("📺 %s", tags))
	}
	if len(meta.Audio) > 0 {
		audio := meta.Audio[0]
		if len(meta.Channels) > 0 {
			audio = fmt.Sprintf("%s %s", audio, meta.Channels[0])
		}
		line2 = append(line2, fmt.Sprintf("🎧 %s", audio))
	}
	if len(line2) > 0 {
		lines = append(lines, strings.Join(line2, " • "))
	}

	flags := []string{}
	if meta.Proper {
		flags = append(flags, "⚡ PROPER")
	}
	if meta.Repack {
		flags = append(flags, "🔄 REPACK")
	}
	if meta.Edition != "" {
		flags = append(flags, fmt.Sprintf("⏱️ %s", strings.ToUpper(meta.Edition)))
	}
	if meta.Unrated {
		flags = append(flags, "🔞 UNRATED")
	}
	if meta.ThreeD {
		flags = append(flags, "🕶️ 3D")
	}
	if len(flags) > 0 {
		lines = append(lines, strings.Join(flags, " "))
	}

	line4 := []string{}
	if sizeGB > 0 {
		line4 = append(line4, fmt.Sprintf("💾 %.2f GB", sizeGB))
	} else {
		line4 = append(line4, "💾 Size Unknown")
	}
	if meta.Group != "" {
		line4 = append(line4, fmt.Sprintf("👥 %s", meta.Group))
	}
	lines = append(lines, strings.Join(line4, " • "))

	if len(meta.Languages) > 0 {
		langs := strings.Join(meta.Languages, " | ")
		lines = append(lines, fmt.Sprintf("🌍 %s", langs))
	}

	lines = append(lines, fmt.Sprintf("📄 %s", filename))

	return strings.Join(lines, "\n")
}
