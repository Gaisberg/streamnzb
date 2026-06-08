package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
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
	FailoverFastMode           bool
}

const maxDirectProbeCandidates = 3

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
	rarScanCtx := WithArchiveFastFailoverMode(ctx, hints.FailoverFastMode)
	rarScanCtx = WithSkipGapProbing(rarScanCtx, true)
	if cachedBP != nil {
		switch bp := cachedBP.(type) {
		case *ArchiveBlueprint:
			if !blueprintTargetMatches(bp.Target, target) {
				logger.Debug("Skipping cached RAR blueprint due to target mismatch", "cached", bp.Target, "requested", target)
				break
			}
			logger.Debug("Using cached RAR blueprint", "cached", bp.Target, "requested", target, "file", bp.MainFileName)
			s, name, size, err := StreamFromBlueprint(rarScanCtx, bp, password)
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
			s, n, sz, err := Open7zStreamFromBlueprint(rarScanCtx, bp, password)
			if err != nil {
				err = maybeMarkArchiveFastProbe(rarScanCtx, err)
			}
			return s, n, sz, bp, err
		case *DirectBlueprint:
			if !blueprintTargetMatches(bp.Target, target) {
				logger.Debug("Skipping cached direct blueprint due to target mismatch", "cached", bp.Target, "requested", target)
				break
			}
			if bp.FileIndex >= 0 && bp.FileIndex < len(files) {
				f := files[bp.FileIndex]
				stream, err := f.OpenStreamCtx(rarScanCtx)
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
	var rarScanErr error
	if len(rarFiles) > 0 {
		logger.Trace("Detected RAR archive", "target", target, "volumes", len(rarFiles))
		unpackables := make([]UnpackableFile, len(files))
		copy(unpackables, files)
		bp, err := ScanArchive(rarScanCtx, unpackables, password, target)
		if err != nil {
			if errors.Is(err, ErrEpisodeTargetNotFound) {
				return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
			}
			rarScanFailed = true
			rarScanErr = maybeMarkArchiveFastProbe(rarScanCtx, err)
			logger.Warn("ScanArchive failed, falling back to other methods", "err", err)
		} else {
			s, name, size, err := StreamFromBlueprint(rarScanCtx, bp, password)
			if err != nil {
				return nil, "", 0, nil, err
			}
			return s, name, size, bp, nil
		}
		logger.Warn("RAR analysis failed; release likely requires PAR2 repair", "target", target, "err", rarScanErr)
	}

	archiveFiles, err := Identify7zParts(files)
	if err != nil && !errors.Is(err, ErrNo7zFiles) {
		logger.Warn("7z archive identification failed", "target", target, "err", err)
		return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
	}
	if len(archiveFiles) > 0 {
		firstVolName := ExtractFilename(archiveFiles[0].Name())
		mode := "full"
		if IsArchiveFastFailoverModeEnabled(rarScanCtx) {
			mode = "fast"
		}
		logger.Info("Detected 7z archive", "target", target, "name", firstVolName, "parts", len(archiveFiles), "mode", mode)
		newBp, err := CreateSevenZipBlueprint(rarScanCtx, archiveFiles, firstVolName, password, target)
		if err != nil {
			err = maybeMarkArchiveFastProbe(rarScanCtx, err)
			if errors.Is(err, ErrEpisodeTargetNotFound) {
				return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
			}
			return nil, "", 0, nil, err
		}
		s, n, sz, err := Open7zStreamFromBlueprint(rarScanCtx, newBp, password)
		if err != nil {
			err = maybeMarkArchiveFastProbe(rarScanCtx, err)
		}
		return s, n, sz, newBp, err
	}

	nameOverrides := recoverDirectFilenamesFromPAR2(ctx, files)
	if len(nameOverrides) > 0 {
		logger.Debug("PAR2 deobfuscation recovered direct media names",
			"renamed_files", len(nameOverrides))
	}

	if directIdx, err := selectDirectFileIndexWithNames(files, target, nameOverrides); err != nil {
		return nil, "", 0, &FailedBlueprint{Err: err, Target: target}, err
	} else if directIdx >= 0 {
		f := files[directIdx]
		name := directFileDisplayName(files, directIdx, nameOverrides)
		stream, err := f.OpenStreamCtx(rarScanCtx)
		if err != nil {
			return nil, "", 0, nil, err
		}
		logger.Debug("Selected direct playback file", "target", target, "name", name, "index", directIdx, "size", f.Size())
		return stream, name, f.Size(), &DirectBlueprint{FileName: name, FileIndex: directIdx, Target: target}, nil
	}

	if probedStream, probedName, probedSize, probedIdx, ok := probeDirectPlayableCandidates(rarScanCtx, files, nameOverrides); ok {
		logger.Debug("Selected direct playback file via content probe",
			"target", target,
			"name", probedName,
			"index", probedIdx,
			"size", probedSize)
		return probedStream, probedName, probedSize, &DirectBlueprint{FileName: probedName, FileIndex: probedIdx, Target: target}, nil
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
			bp, err := ScanArchive(rarScanCtx, unpackables, password, target)
			if err == nil {
				s, name, size, err := StreamFromBlueprint(rarScanCtx, bp, password)
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
		extractedName := directFileDisplayName(files, largestIdx, nameOverrides)
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
		stream, err := largestFile.OpenStreamCtx(rarScanCtx)
		if err != nil {
			return nil, "", 0, nil, err
		}
		logger.Debug("Falling back to largest direct file", "target", target, "name", extractedName, "index", largestIdx, "size", largestFile.Size())
		return stream, extractedName, largestFile.Size(), &DirectBlueprint{FileName: extractedName, FileIndex: largestIdx, Target: target}, nil
	}

	logger.Warn("GetMediaStream found no suitable media", "target", target, "files", len(files), "rar_candidates", len(rarFiles))
	finalErr := io.EOF
	if rarScanErr != nil {
		finalErr = fmt.Errorf("no suitable media stream: RAR scan failed: %w", rarScanErr)
	} else {
		finalErr = fmt.Errorf("no suitable media stream: no direct video candidates and no playable archive content")
	}
	return nil, "", 0, &FailedBlueprint{Err: finalErr, Target: target}, finalErr
}

type directProbeCandidate struct {
	idx  int
	file UnpackableFile
	name string
	size int64
}

func probeDirectPlayableCandidates(ctx context.Context, files []UnpackableFile, nameOverrides map[int]string) (ReadSeekCloser, string, int64, int, bool) {
	candidates := make([]directProbeCandidate, 0, len(files))
	for i, f := range files {
		if f == nil {
			continue
		}
		name := directFileDisplayName(files, i, nameOverrides)
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ExtRar) || strings.Contains(lower, ".part") || IsRarPart(lower) || IsSplitArchivePart(lower) {
			continue
		}
		if strings.HasSuffix(lower, ExtPar2) || strings.HasSuffix(lower, ExtNzb) || strings.HasSuffix(lower, ExtNfo) || strings.HasSuffix(lower, ExtIso) {
			continue
		}
		size := f.Size()
		if size < 50*1024*1024 {
			continue
		}
		candidates = append(candidates, directProbeCandidate{idx: i, file: f, name: name, size: size})
	}
	if len(candidates) == 0 {
		return nil, "", 0, -1, false
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].size > candidates[j].size })
	if len(candidates) > maxDirectProbeCandidates {
		candidates = candidates[:maxDirectProbeCandidates]
	}

	for _, c := range candidates {
		if err := contextErr(ctx); err != nil {
			return nil, "", 0, -1, false
		}
		stream, err := c.file.OpenStreamCtx(ctx)
		if err != nil {
			continue
		}
		probeErr := ProbeMediaStreamByContent(stream, c.name, c.size)
		if probeErr == nil {
			return stream, c.name, c.size, c.idx, true
		}
		_ = stream.Close()
	}
	return nil, "", 0, -1, false
}
