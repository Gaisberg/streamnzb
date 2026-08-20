package triage

import (
	"sort"
	"time"

	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
)

type Candidate struct {
	Release     *release.Release
	Metadata    *parser.ParsedRelease
	Group       string
	Score       int
	QuerySource string

	// Verdict is what the pipeline decided about this release beyond its
	// score: the content kind it was ranked as, why a profile rejected it,
	// which named rules paid out, what ffprobe measured, and what the
	// community availability database knows. Stages fill their own part; an
	// unset field means that stage did not run or had nothing to say.
	Verdict Verdict
}

type Service struct{}

func NewService() *Service {
	return &Service{}
}

func (s *Service) SortCandidates(candidates []Candidate) {
	for i := range candidates {
		rel := candidates[i].Release
		if rel == nil {
			continue
		}
		parsed := parser.ParseReleaseTitle(rel.Title)
		group := parsed.ResolutionGroup()
		score := basicScore(rel)
		querySource := rel.QuerySource
		if querySource == "" {
			querySource = "id"
		}
		candidates[i].Metadata = parsed
		candidates[i].Group = group
		candidates[i].Score = score
		candidates[i].QuerySource = querySource
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
}

func basicScore(rel *release.Release) int {
	score := 0
	if rel != nil && rel.IsLibraryResult() {
		score += 500
	}

	// Size score: larger files score higher
	sizeGB := float64(rel.Size) / (1024 * 1024 * 1024)
	if sizeGB > 100 {
		score += 9000
	} else if sizeGB > 50 {
		score += 8000
	} else if sizeGB > 20 {
		score += 7000
	} else if sizeGB > 10 {
		score += 6000
	} else if sizeGB > 5 {
		score += 5000
	} else if sizeGB > 2 {
		score += 4000
	} else if sizeGB > 1 {
		score += 3000
	} else if sizeGB > 0.5 {
		score += 2000
	} else if sizeGB > 0 {
		score += 1000
	}

	// Age score: newer releases score higher
	if rel.PubDate != "" {
		pubTime, err := time.Parse(time.RFC1123Z, rel.PubDate)
		if err != nil {
			pubTime, err = time.Parse(time.RFC1123, rel.PubDate)
		}
		if err == nil {
			ageHours := time.Since(pubTime).Hours()
			ageScore := int(10000.0 - ageHours)
			if ageScore < 0 {
				ageScore = 0
			}
			score += ageScore
		}
	}

	// Grabs score
	score += rel.Grabs

	return score
}
