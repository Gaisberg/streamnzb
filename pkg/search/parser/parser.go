package parser

import (
	"regexp"
	"strconv"
	"strings"

	"streamnzb/pkg/core/config/pttoptions"

	"github.com/MunifTanjim/go-ptt"
)

var dashedSeasonEpisodePattern = regexp.MustCompile(`(?i)\bS(?:eason)?\s*0*([0-9]{1,2})\s*-\s*0*([0-9]{1,3})(?:$|[\s._()[\]])`)

type ParsedRelease struct {
	Title      string
	Year       int
	Resolution string
	Quality    string
	Codec      string
	Audio      []string
	Channels   []string
	HDR        []string
	Container  string
	Group      string
	Season     int
	Episode    int
	Seasons    []int
	Episodes   []int

	Languages []string
	Network   string
	Repack    bool
	Proper    bool
	Extended  bool
	Unrated   bool
	ThreeD    string
	Size      string
	BitDepth  string
	Dubbed    bool
	Hardcoded bool

	Edition      string
	Date         string
	Commentary   bool
	Complete     bool
	Convert      bool
	Documentary  bool
	Remastered   bool
	Retail       bool
	Subbed       bool
	Uncensored   bool
	Upscaled     bool
	Region       string
	ReleaseTypes []string
	EpisodeCode  string
	Site         string
	Extension    string
	Volumes      []int
}

func ParseReleaseTitle(title string) *ParsedRelease {
	info := ptt.Parse(title)

	codec := pttoptions.NormalizeCodec(info.Codec)
	if codec == "" && info.Codec != "" {
		codec = info.Codec
	}

	parsed := &ParsedRelease{
		Title:        info.Title,
		Resolution:   info.Resolution,
		Quality:      info.Quality,
		Codec:        codec,
		Audio:        info.Audio,
		Channels:     info.Channels,
		HDR:          info.HDR,
		Container:    info.Container,
		Group:        info.Group,
		Languages:    info.Languages,
		Network:      info.Network,
		Repack:       info.Repack,
		Proper:       info.Proper,
		Extended:     info.Extended,
		Unrated:      info.Unrated,
		ThreeD:       info.ThreeD,
		Size:         info.Size,
		BitDepth:     info.BitDepth,
		Dubbed:       info.Dubbed,
		Hardcoded:    info.Hardcoded,
		Edition:      info.Edition,
		Date:         info.Date,
		Commentary:   info.Commentary,
		Complete:     info.Complete,
		Convert:      info.Convert,
		Documentary:  info.Documentary,
		Remastered:   info.Remastered,
		Retail:       info.Retail,
		Subbed:       info.Subbed,
		Uncensored:   info.Uncensored,
		Upscaled:     info.Upscaled,
		Region:       info.Region,
		ReleaseTypes: info.ReleaseTypes,
		EpisodeCode:  info.EpisodeCode,
		Site:         info.Site,
		Extension:    info.Extension,
		Seasons:      uniqueInts(info.Seasons),
		Episodes:     uniqueInts(info.Episodes),
		Volumes:      info.Volumes,
	}

	if info.Year != "" {
		if year, err := strconv.Atoi(info.Year); err == nil {
			parsed.Year = year
		}
	}
	if len(parsed.Seasons) > 0 {
		parsed.Season = parsed.Seasons[0]
	}
	if len(parsed.Episodes) > 0 {
		parsed.Episode = parsed.Episodes[0]
	}
	applyDashedSeasonEpisodeFallback(title, parsed)

	return parsed
}

func applyDashedSeasonEpisodeFallback(rawTitle string, parsed *ParsedRelease) {
	if parsed == nil {
		return
	}
	matches := dashedSeasonEpisodePattern.FindStringSubmatch(rawTitle)
	if len(matches) != 3 {
		return
	}
	season, seasonErr := strconv.Atoi(matches[1])
	episode, episodeErr := strconv.Atoi(matches[2])
	if seasonErr != nil || episodeErr != nil || season <= 0 || episode <= 0 {
		return
	}
	if parsed.Season == 0 {
		if !hasInt(parsed.Seasons, season) {
			parsed.Seasons = append(parsed.Seasons, season)
		}
		parsed.Season = season
	}
	if parsed.Episode == 0 {
		if !hasInt(parsed.Episodes, episode) {
			parsed.Episodes = append(parsed.Episodes, episode)
		}
		parsed.Episode = episode
	}
	if parsed.Title != "" {
		cleanedTitle := strings.TrimSpace(dashedSeasonEpisodePattern.ReplaceAllString(parsed.Title, " "))
		cleanedTitle = strings.Join(strings.Fields(cleanedTitle), " ")
		if cleanedTitle != "" {
			parsed.Title = cleanedTitle
		}
	}
}

func uniqueInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func hasInt(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func (p *ParsedRelease) HasSeason(season int) bool {
	if p == nil || season <= 0 {
		return false
	}
	if hasInt(p.Seasons, season) {
		return true
	}
	return p.Season == season
}

func (p *ParsedRelease) HasEpisode(episode int) bool {
	if p == nil || episode <= 0 {
		return false
	}
	if hasInt(p.Episodes, episode) {
		return true
	}
	return p.Episode == episode
}

func (p *ParsedRelease) IsEpisodeRelease(season, episode int) bool {
	if p == nil || season <= 0 || episode <= 0 {
		return false
	}
	return p.HasSeason(season) && p.HasEpisode(episode) && len(p.Episodes) <= 1
}

func (p *ParsedRelease) IsMultiEpisodeRelease(season, episode int) bool {
	if p == nil || season <= 0 || episode <= 0 {
		return false
	}
	return p.HasSeason(season) && p.HasEpisode(episode) && len(p.Episodes) > 1
}

func (p *ParsedRelease) IsSeasonPack(season int) bool {
	if p == nil || season <= 0 {
		return false
	}
	if !p.HasSeason(season) || len(p.Episodes) > 0 {
		return false
	}
	return !p.IsShowPack()
}

func (p *ParsedRelease) IsShowPack() bool {
	if p == nil || !p.Complete || len(p.Episodes) > 0 {
		return false
	}
	return len(p.Seasons) == 0 || len(p.Seasons) > 1
}

func (p *ParsedRelease) EpisodeMatchRank(season, episode int) int {
	if p == nil || season <= 0 || episode <= 0 {
		return 0
	}
	if p.IsEpisodeRelease(season, episode) {
		return 4
	}
	if p.IsMultiEpisodeRelease(season, episode) {
		return 3
	}
	if p.IsSeasonPack(season) {
		return 2
	}
	if p.IsShowPack() {
		return 1
	}
	return 0
}

func (p *ParsedRelease) MatchesEpisodeRequest(season, episode int) bool {
	return p.EpisodeMatchRank(season, episode) > 0
}

func (p *ParsedRelease) ResolutionGroup() string {
	if p == nil {
		return "sd"
	}
	res := strings.ToLower(p.Resolution)
	if strings.Contains(res, "2160") || strings.Contains(res, "4k") {
		return "4k"
	}
	if strings.Contains(res, "1080") {
		return "1080p"
	}
	if strings.Contains(res, "720") {
		return "720p"
	}
	return "sd"
}
