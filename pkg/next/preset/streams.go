package preset

import (
	"context"
	"fmt"
	"strings"
)

type StreamResponse struct {
	Streams []Stream `json:"streams"`
}

type Stream struct {
	NZBURL        string         `json:"nzbUrl,omitempty"`
	URL           string         `json:"url,omitempty"`
	Name          string         `json:"name,omitempty"`
	Title         string         `json:"title,omitempty"`
	Description   string         `json:"description,omitempty"`
	BehaviorHints *BehaviorHints `json:"behaviorHints,omitempty"`
}

type BehaviorHints struct {
	NotWebReady bool   `json:"notWebReady,omitempty"`
	BingeGroup  string `json:"bingeGroup,omitempty"`
	VideoSize   int64  `json:"videoSize,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Cached      *bool  `json:"cached,omitempty"`
}

func (s *Service) StreamsWithNZBURLResolver(ctx context.Context, req MatchRequest, resolveNZBURL func(Candidate) (string, error)) (StreamResponse, error) {
	resp, err := s.Match(ctx, req)
	if err != nil {
		return StreamResponse{}, err
	}
	streams, err := buildStreams(req, resp.Candidates, resolveNZBURL)
	if err != nil {
		return StreamResponse{}, err
	}
	return StreamResponse{Streams: streams}, nil
}

func buildStreams(req MatchRequest, candidates []Candidate, resolveNZBURL func(Candidate) (string, error)) ([]Stream, error) {
	streams := make([]Stream, 0, len(candidates))
	for _, cand := range candidates {
		stream, err := buildStream(req, cand, resolveNZBURL)
		if err != nil {
			return nil, err
		}
		streams = append(streams, stream)
	}
	return streams, nil
}

func buildStream(req MatchRequest, cand Candidate, resolveNZBURL func(Candidate) (string, error)) (Stream, error) {
	nzbURL, err := resolveNZBURL(cand)
	if err != nil {
		return Stream{}, err
	}
	return Stream{
		NZBURL:      nzbURL,
		Name:        buildStreamName(cand),
		Title:       cand.Title,
		Description: buildStreamDescription(cand),
		BehaviorHints: &BehaviorHints{
			NotWebReady: true,
			BingeGroup:  buildBingeGroup(req),
			VideoSize:   cand.Size,
			Filename:    cand.Title,
			Cached:      availabilityCached(cand.Availability),
		},
	}, nil
}
func buildStreamName(cand Candidate) string {
	if strings.EqualFold(strings.TrimSpace(cand.Availability), "Available") {
		return "⚡ StreamNZB"
	}
	return "StreamNZB"
}

func buildStreamDescription(cand Candidate) string {
	lines := []string{"StreamNZB", cand.Title}
	if cand.Indexer != "" {
		lines = append(lines, "Indexer: "+cand.Indexer)
	}
	if size := formatSize(cand.Size); size != "" {
		lines = append(lines, "Size: "+size)
	}
	if cand.Availability != "" {
		lines = append(lines, "Availability: "+cand.Availability)
	}
	return strings.Join(lines, "\n")
}

func buildBingeGroup(req MatchRequest) string {
	return "streamnzb-next|" + strings.ToLower(strings.TrimSpace(req.Type)) + "|" + strings.TrimSpace(req.MetadataID)
}

func availabilityCached(availability string) *bool {
	switch strings.ToLower(strings.TrimSpace(availability)) {
	case "available":
		v := true
		return &v
	case "unavailable":
		v := false
		return &v
	default:
		return nil
	}
}

func formatSize(size int64) string {
	if size <= 0 {
		return ""
	}
	const gb = 1024 * 1024 * 1024
	const mb = 1024 * 1024
	if size >= gb {
		return fmt.Sprintf("%.2f GB", float64(size)/gb)
	}
	return fmt.Sprintf("%.1f MB", float64(size)/mb)
}
