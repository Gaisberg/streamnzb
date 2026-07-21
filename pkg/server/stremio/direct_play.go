package stremio

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/nzb"
)

const maxDirectPlayNZBSize = 16 << 20

func (s *Server) CreateDirectPlaySessionFromURL(ctx context.Context, nzbURL string, stream *auth.Stream) (string, error) {
	trimmedURL := strings.TrimSpace(nzbURL)
	if trimmedURL == "" {
		return "", fmt.Errorf("missing NZB URL")
	}
	parsedURL, err := neturl.Parse(trimmedURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return "", fmt.Errorf("invalid NZB URL")
	}

	data, err := s.downloadNZBForDirectPlay(ctx, trimmedURL)
	if err != nil {
		return "", err
	}
	return s.CreateDirectPlaySessionFromNZBData(ctx, trimmedURL, data, stream)
}

func (s *Server) CreateDirectPlaySessionFromNZBData(ctx context.Context, sourceName string, nzbData []byte, stream *auth.Stream) (string, error) {
	if s == nil || s.sessionManager == nil {
		return "", fmt.Errorf("session manager unavailable")
	}
	if len(nzbData) == 0 {
		return "", fmt.Errorf("empty NZB payload")
	}
	if len(nzbData) > maxDirectPlayNZBSize {
		return "", fmt.Errorf("NZB payload too large")
	}

	parsedNZB, err := nzb.ParseWithContext(ctx, bytes.NewReader(nzbData))
	if err != nil {
		return "", fmt.Errorf("failed to parse NZB: %w", err)
	}

	sessionID, err := newDirectPlaySessionID()
	if err != nil {
		return "", err
	}

	sess, err := s.sessionManager.CreateSessionWithFetcher(
		sessionID,
		parsedNZB,
		nil,
		nil,
		s.segmentFetcherForStream(stream),
		s.providerHostsForStream(stream),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	sess.StreamName = streamID(stream)
	sess.ContentType = "direct"
	sess.ContentID = sessionID
	sess.ContentTitle = strings.TrimSpace(sourceName)
	return sessionID, nil
}

func (s *Server) DirectPlayURLForSession(sessionID string, stream *auth.Stream) string {
	return s.baseURLWithToken(stream) + "/play/" + sessionID
}

func (s *Server) DirectPlayPathForSession(sessionID string, stream *auth.Stream) string {
	if stream != nil && strings.TrimSpace(stream.Token) != "" {
		return "/" + strings.TrimSpace(stream.Token) + "/play/" + sessionID
	}
	return "/play/" + sessionID
}

func (s *Server) downloadNZBForDirectPlay(ctx context.Context, nzbURL string) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("playback server unavailable")
	}
	dlCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var nzbData []byte
	var err error
	if s.indexer != nil {
		nzbData, err = s.indexer.DownloadNZB(dlCtx, nzbURL)
		if err == nil && len(nzbData) > 0 {
			if len(nzbData) > maxDirectPlayNZBSize {
				return nil, fmt.Errorf("downloaded NZB exceeds max size")
			}
			return nzbData, nil
		}
	}

	req, reqErr := http.NewRequestWithContext(dlCtx, http.MethodGet, nzbURL, nil)
	if reqErr != nil {
		return nil, reqErr
	}
	resp, httpErr := http.DefaultClient.Do(req)
	if httpErr != nil {
		if err != nil {
			return nil, fmt.Errorf("failed to download NZB: %w", err)
		}
		return nil, fmt.Errorf("failed to download NZB: %w", httpErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download NZB (HTTP %d)", resp.StatusCode)
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxDirectPlayNZBSize+1))
	if readErr != nil {
		return nil, fmt.Errorf("failed to read NZB body: %w", readErr)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("downloaded NZB is empty")
	}
	if len(body) > maxDirectPlayNZBSize {
		return nil, fmt.Errorf("downloaded NZB exceeds max size")
	}
	return body, nil
}

func newDirectPlaySessionID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		logger.Error("Failed to generate direct play session id", "err", err)
		return "", fmt.Errorf("failed to generate session id")
	}
	return "direct-" + hex.EncodeToString(buf), nil
}
