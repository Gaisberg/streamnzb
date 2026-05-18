package fileutil

import "strings"

func ExtractFilename(subject string) string {

	if start := strings.Index(subject, "\""); start != -1 {
		if end := strings.Index(subject[start+1:], "\""); end != -1 {
			return strings.Trim(subject[start+1:start+1+end], "\"' ")
		}
	}

	clean := strings.TrimSpace(subject)

	if idx := strings.LastIndex(clean, " ("); idx != -1 {
		suffix := clean[idx:]
		if strings.Contains(suffix, "/") && strings.HasSuffix(suffix, ")") {
			clean = strings.TrimSpace(clean[:idx])
		}
	}
	if idx := strings.LastIndex(clean, " ["); idx != -1 {
		suffix := clean[idx:]
		if strings.Contains(suffix, "/") && strings.HasSuffix(suffix, "]") {
			clean = strings.TrimSpace(clean[:idx])
		}
	}

	clean = strings.TrimSuffix(clean, " yEnc")
	clean = strings.TrimSpace(clean)

	if idx := strings.LastIndex(clean, "/"); idx != -1 {
		clean = clean[idx+1:]
	}
	if idx := strings.LastIndex(clean, "\\"); idx != -1 {
		clean = clean[idx+1:]
	}

	return strings.Trim(clean, "\"' ")
}

var videoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".mov": true, ".wmv": true, ".flv": true, ".webm": true,
	".mpg": true, ".mpeg": true, ".vob": true,
}

func IsVideoExtension(ext string) bool {
	return videoExtensions[strings.ToLower(ext)]
}

func IsVideoFile(name string) bool {
	lower := strings.ToLower(name)
	for ext := range videoExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func IsVideoOrArchiveExtension(ext string) bool {
	ext = strings.ToLower(ext)
	if videoExtensions[ext] {
		return true
	}
	switch ext {
	case ".rar", ".7z":
		return true
	}

	if len(ext) == 4 && ext[0] == '.' && ext[1] == 'r' && ext[2] >= '0' && ext[2] <= '9' && ext[3] >= '0' && ext[3] <= '9' {
		return true
	}
	return false
}
