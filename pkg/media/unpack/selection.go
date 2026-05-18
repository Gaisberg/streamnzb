package unpack

import (
	"fmt"
	"path/filepath"

	"streamnzb/pkg/core/logger"
	searchparser "streamnzb/pkg/search/parser"
)

type EpisodeTarget struct {
	Season  int
	Episode int
}

func (t EpisodeTarget) Valid() bool {
	return t.Season > 0 && t.Episode > 0
}

type namedEpisodeCandidate struct {
	Name  string
	Size  int64
	Index int
	Order int
}

func selectEpisodeCandidate(candidates []namedEpisodeCandidate, target EpisodeTarget) (namedEpisodeCandidate, bool) {
	if !target.Valid() {
		return namedEpisodeCandidate{}, false
	}
	var best namedEpisodeCandidate
	bestRank := 0
	found := false
	for _, candidate := range candidates {
		rank := episodeNameMatchRank(candidate.Name, target)
		if rank == 0 {
			continue
		}
		if !found || rank > bestRank ||
			(rank == bestRank && (candidate.Size > best.Size ||
				(candidate.Size == best.Size && candidate.Order < best.Order))) {
			best = candidate
			bestRank = rank
			found = true
		}
	}
	if found {
		logger.Debug("Unpack episode candidate selected",
			"target", target,
			"name", best.Name,
			"rank", bestRank,
			"size", best.Size,
			"order", best.Order,
			"candidates", len(candidates))
	} else {
		logger.Debug("Unpack episode candidate selection found no match",
			"target", target,
			"candidates", len(candidates))
	}
	return best, found
}

func selectEpisodeCandidateOrError(candidates []namedEpisodeCandidate, target EpisodeTarget, scope string) (namedEpisodeCandidate, bool, error) {
	if best, ok := selectEpisodeCandidate(candidates, target); ok {
		return best, true, nil
	}
	if !target.Valid() || len(candidates) == 0 {
		return namedEpisodeCandidate{}, false, nil
	}
	if len(candidates) == 1 {
		baseName := filepath.Base(candidates[0].Name)
		parsed := searchparser.ParseReleaseTitle(baseName)
		if parsed == nil || (parsed.Season == 0 && parsed.Episode == 0 && len(parsed.Episodes) == 0) {
			logger.Debug("Skipping episode title check for single-candidate release with unparseable filename",
				"target", target,
				"scope", scope,
				"name", candidates[0].Name)
			return namedEpisodeCandidate{}, false, nil
		}
	}
	err := fmt.Errorf("%w: season=%d episode=%d scope=%s candidates=%d", ErrEpisodeTargetNotFound, target.Season, target.Episode, scope, len(candidates))
	logger.Warn("Requested episode not found in candidate set",
		"target", target,
		"scope", scope,
		"candidates", len(candidates),
		"err", err)
	return namedEpisodeCandidate{}, false, err
}

func episodeNameMatchRank(name string, target EpisodeTarget) int {
	if !target.Valid() {
		return 0
	}
	baseName := filepath.Base(name)
	parsed := searchparser.ParseReleaseTitle(baseName)
	if parsed == nil {
		logger.Debug("Unpack episode candidate parse returned nil",
			"target", target,
			"name", baseName)
		return 0
	}
	rank := parsed.EpisodeMatchRank(target.Season, target.Episode)
	logger.Debug("Unpack episode candidate rank evaluated",
		"target", target,
		"name", baseName,
		"rank", rank,
		"parsed_season", parsed.Season,
		"parsed_episode", parsed.Episode,
		"parsed_seasons", parsed.Seasons,
		"parsed_episodes", parsed.Episodes,
		"complete", parsed.Complete,
		"episode_code", parsed.EpisodeCode)
	return rank
}

func selectDirectFileIndex(files []UnpackableFile, target EpisodeTarget) (int, error) {
	firstVideoIdx := -1
	firstVideoName := ""
	candidates := make([]namedEpisodeCandidate, 0, len(files))
	for i, f := range files {
		name := ExtractFilename(f.Name())
		if !IsVideoFile(name) {
			continue
		}
		if firstVideoIdx == -1 {
			firstVideoIdx = i
			firstVideoName = name
		}
		candidates = append(candidates, namedEpisodeCandidate{Name: name, Size: f.Size(), Index: i, Order: len(candidates)})
	}
	if best, ok, err := selectEpisodeCandidateOrError(candidates, target, "direct_media"); err != nil {
		return -1, err
	} else if ok {
		logger.Debug("Direct file selection matched requested episode",
			"target", target,
			"name", best.Name,
			"index", best.Index,
			"size", best.Size,
			"candidates", len(candidates))
		return best.Index, nil
	}
	if firstVideoIdx >= 0 {
		logger.Debug("Direct file selection fell back to first video",
			"target", target,
			"name", firstVideoName,
			"index", firstVideoIdx,
			"candidates", len(candidates))
	} else {
		logger.Debug("Direct file selection found no video candidates",
			"target", target,
			"files", len(files))
	}
	return firstVideoIdx, nil
}
