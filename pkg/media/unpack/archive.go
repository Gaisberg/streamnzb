package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"

	"streamnzb/pkg/core/logger"
)

var ErrTooManyZeroFills = errors.New("too many failed segments")
var ErrEpisodeTargetNotFound = errors.New("requested episode not found in release")

type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

type DirectBlueprint struct {
	FileName  string
	FileIndex int
	Target    EpisodeTarget
}

type FailedBlueprint struct {
	Err    error
	Target EpisodeTarget
}

type StreamSelectionHints struct {
	AllowLargestDirectFallback bool
}

func isPlausibleLargestDirectFallbackName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}

	lower := strings.ToLower(trimmed)
	if IsVideoFile(lower) {
		return true
	}

	switch strings.ToLower(filepath.Ext(trimmed)) {
	case ".m2ts", ".mts", ".ts", ".tp":
		return true
	}

	if filepath.Ext(trimmed) != "" {
		return false
	}

	if utfLen := len([]rune(trimmed)); utfLen < 6 {
		return false
	}

	letters := 0
	alphaNum := 0
	for _, r := range trimmed {
		switch {
		case unicode.IsLetter(r):
			letters++
			alphaNum++
		case unicode.IsDigit(r):
			alphaNum++
		case strings.ContainsRune(" ._[]()-,'&+", r):
			continue
		default:
			return false
		}
	}

	return letters > 0 && alphaNum >= 6
}

func blueprintTargetMatches(cachedTarget, requestedTarget EpisodeTarget) bool {
	return cachedTarget == requestedTarget
}

func GetMediaStream(ctx context.Context, files []UnpackableFile, cachedBP interface{}, password string) (ReadSeekCloser, string, int64, interface{}, error) {
	return GetMediaStreamForEpisodeWithHints(ctx, files, cachedBP, password, EpisodeTarget{}, StreamSelectionHints{AllowLargestDirectFallback: true})
}

func GetMediaStreamForEpisode(ctx context.Context, files []UnpackableFile, cachedBP interface{}, password string, target EpisodeTarget) (ReadSeekCloser, string, int64, interface{}, error) {
	return GetMediaStreamForEpisodeWithHints(ctx, files, cachedBP, password, target, StreamSelectionHints{AllowLargestDirectFallback: true})
}

func GetMediaStreamForEpisodeWithHints(ctx context.Context, files []UnpackableFile, cachedBP interface{}, password string, target EpisodeTarget, hints StreamSelectionHints) (ReadSeekCloser, string, int64, interface{}, error) {
	if err := contextErr(ctx); err != nil {
		return nil, "", 0, nil, err
	}
	logger.Debug("GetMediaStreamForEpisode starting",
		"target", target,
		"files", len(files),
		"cached_type", fmt.Sprintf("%T", cachedBP))
	if cachedBP != nil {
		switch bp := cachedBP.(type) {
		case *ArchiveBlueprint:
			if !blueprintTargetMatches(bp.Target, target) {
				logger.Debug("Skipping cached RAR blueprint due to target mismatch", "cached", bp.Target, "requested", target)
				break
			}
			logger.Debug("Using cached RAR blueprint", "cached", bp.Target, "requested", target, "file", bp.MainFileName)
			s, name, size, err := StreamFromBlueprint(ctx, bp, password)
			return s, name, size, bp, err
		case *SevenZipBlueprint:
			if !blueprintTargetMatches(bp.Target, target) {
				logger.Debug("Skipping cached 7z blueprint due to target mismatch", "cached", bp.Target, "requested", target)
				break
			}
			logger.Debug("Using cached 7z blueprint", "cached", bp.Target, "requested", target, "file", bp.MainFileName)
			if len(bp.Files) == 0 {
				return nil, "", 0, nil, errors.New("7z set empty for cached blueprint")
			}
			s, n, sz, err := Open7zStreamFromBlueprint(ctx, bp, password)
			return s, n, sz, bp, err
		case *DirectBlueprint:
			if !blueprintTargetMatches(bp.Target, target) {
				logger.Debug("Skipping cached direct blueprint due to target mismatch", "cached", bp.Target, "requested", target)
				break
			}
			if bp.FileIndex >= 0 && bp.FileIndex < len(files) {
				f := files[bp.FileIndex]
				stream, err := f.OpenStreamCtx(ctx)
				if err != nil {
					return nil, "", 0, nil, err
				}
				logger.Debug("Using cached direct blueprint", "cached", bp.Target, "requested", target, "file", bp.FileName, "index", bp.FileIndex)
				return stream, bp.FileName, f.Size(), bp, nil
			}
		case *FailedBlueprint:
			if !blueprintTargetMatches(bp.Target, target) {
				logger.Debug("Skipping cached scan failure due to target mismatch", "cached", bp.Target, "requested", target)
				break
			}
			logger.Debug("Using cached scan failure", "cached", bp.Target, "requested", target, "err", bp.Err)
			return nil, "", 0, bp, bp.Err
		}
	}

	rarFiles := filterRarFiles(files)
	var rarScanFailed bool
	if len(rarFiles) > 0 {
		logger.Trace("Detected RAR archive", "target", target, "volumes", len(rarFiles))
		unpackables := make([]UnpackableFile, len(files))
		copy(unpackables, files)
		bp, err := ScanArchive(ctx, unpackables, password, target)
		if err != nil {
			if errors.Is(err, ErrEpisodeTargetNotFound) {
				return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
			}
			rarScanFailed = true
			logger.Warn("ScanArchive failed, falling back to other methods", "err", err)
		} else {
			s, name, size, err := StreamFromBlueprint(ctx, bp, password)
			if err != nil {
				return nil, "", 0, nil, err
			}
			return s, name, size, bp, nil
		}
	}

	archiveFiles, err := Identify7zParts(files)
	if err == nil && len(archiveFiles) > 0 {
		firstVolName := ExtractFilename(archiveFiles[0].Name())
		logger.Info("Detected 7z archive", "target", target, "name", firstVolName, "parts", len(archiveFiles))
		newBp, err := CreateSevenZipBlueprint(ctx, archiveFiles, firstVolName, password, target)
		if err != nil {
			if errors.Is(err, ErrEpisodeTargetNotFound) {
				return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
			}
			return nil, "", 0, nil, err
		}
		s, n, sz, err := Open7zStreamFromBlueprint(ctx, newBp, password)
		return s, n, sz, newBp, err
	}

	if directIdx, err := selectDirectFileIndex(files, target); err != nil {
		return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
	} else if directIdx >= 0 {
		f := files[directIdx]
		name := ExtractFilename(f.Name())
		stream, err := f.OpenStreamCtx(ctx)
		if err != nil {
			return nil, "", 0, nil, err
		}
		logger.Debug("Selected direct playback file", "target", target, "name", name, "index", directIdx, "size", f.Size())
		return stream, name, f.Size(), &DirectBlueprint{FileName: name, FileIndex: directIdx, Target: target}, nil
	}

	var largestFile UnpackableFile
	var largestIdx int
	for i, f := range files {
		name := strings.ToLower(ExtractFilename(f.Name()))
		if strings.HasSuffix(name, ExtRar) || strings.Contains(name, ".part") || IsRarPart(name) || IsSplitArchivePart(name) {
			continue
		}
		if strings.HasSuffix(name, ExtPar2) || strings.HasSuffix(name, ExtNzb) || strings.HasSuffix(name, ExtNfo) || strings.HasSuffix(name, ExtIso) {
			continue
		}
		if largestFile == nil || f.Size() > largestFile.Size() {
			largestFile = f
			largestIdx = i
		}
	}

	if largestFile != nil && largestFile.Size() > 50*1024*1024 {
		if !rarScanFailed {
			logger.Warn("No clear media found, probing largest file", "name", largestFile.Name(), "size", largestFile.Size())
			unpackables := make([]UnpackableFile, len(files))
			copy(unpackables, files)
			logger.Info("Attempting heuristic RAR scan on unknown files")
			bp, err := ScanArchive(ctx, unpackables, password, target)
			if err == nil {
				s, name, size, err := StreamFromBlueprint(ctx, bp, password)
				if err == nil {
					logger.Info("Heuristic scan found RAR archive")
					return s, name, size, bp, nil
				}
			} else if errors.Is(err, ErrEpisodeTargetNotFound) {
				return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
			} else {
				logger.Warn("Heuristic RAR scan failed, falling back to direct stream", "err", err)
			}
		}
		extractedName := ExtractFilename(largestFile.Name())
		if !hints.AllowLargestDirectFallback {
			err := fmt.Errorf("%w: largest direct fallback disabled", io.EOF)
			if target.Valid() {
				err = fmt.Errorf("%w: no direct media candidate matched season=%d episode=%d", ErrEpisodeTargetNotFound, target.Season, target.Episode)
				logger.Warn("Refusing largest-file fallback for targeted episode request",
					"target", target,
					"name", extractedName,
					"index", largestIdx,
					"size", largestFile.Size())
			} else {
				logger.Warn("Refusing largest-file fallback by selection hints",
					"target", target,
					"name", extractedName,
					"index", largestIdx,
					"size", largestFile.Size())
			}
			return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
		}
		if !isPlausibleLargestDirectFallbackName(extractedName) {
			err := fmt.Errorf("%w: suspicious largest direct fallback candidate %q", io.EOF, extractedName)
			logger.Warn("Refusing suspicious largest-file fallback",
				"target", target,
				"name", extractedName,
				"index", largestIdx,
				"size", largestFile.Size())
			return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
		}
		stream, err := largestFile.OpenStreamCtx(ctx)
		if err != nil {
			return nil, "", 0, nil, err
		}
		logger.Debug("Falling back to largest direct file", "target", target, "name", extractedName, "index", largestIdx, "size", largestFile.Size())
		return stream, extractedName, largestFile.Size(), &DirectBlueprint{FileName: extractedName, FileIndex: largestIdx, Target: target}, nil
	}

	logger.Warn("GetMediaStream found no suitable media", "target", target, "files", len(files), "rar_candidates", len(rarFiles))
	return nil, "", 0, &FailedBlueprint{Err: io.EOF, Target: target}, io.EOF
}
