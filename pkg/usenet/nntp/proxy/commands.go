package proxy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
)

const poolGetTimeout = 60 * time.Second

func isClientWriteError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "forcibly closed") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "wsasend") ||
		strings.Contains(msg, "use of closed network connection")
}

func normalizeMessageID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, "<") {
		s = "<" + s
	}
	if !strings.HasSuffix(s, ">") {
		s = s + ">"
	}
	return s
}

func (s *Session) handleQuit(args []string) error {
	s.shouldQuit = true
	return s.WriteLine("205 Closing connection")
}

func (s *Session) handleCapabilities(args []string) error {
	capabilities := []string{
		"101 Capability list:",
		"VERSION 2",
		"READER",
		"LIST ACTIVE",
	}

	// RFC 4643: AUTHINFO must not be advertised after successful authentication.
	if s.authUser != "" && !s.authenticated {
		capabilities = append(capabilities, "AUTHINFO USER")
	}

	return s.WriteMultiLine(capabilities)
}

func (s *Session) handleAuthInfo(args []string) error {
	if len(args) < 2 {
		return s.WriteLine("501 Syntax error")
	}

	subCmd := strings.ToUpper(args[0])
	value := args[1]

	switch subCmd {
	case "USER":
		if s.authUser == "" {

			s.authenticated = true
			return s.WriteLine("281 Authentication accepted")
		}

		if value == s.authUser {
			return s.WriteLine("381 Password required")
		}
		return s.WriteLine("481 Authentication failed")

	case "PASS":
		if value == s.authPass {
			s.authenticated = true
			return s.WriteLine("281 Authentication accepted")
		}
		return s.WriteLine("481 Authentication failed")

	default:
		return s.WriteLine("501 Syntax error")
	}
}

func (s *Session) handleGroup(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	groupName := args[0]
	s.currentGroup = groupName

	return s.WriteLine(fmt.Sprintf("211 0 1 1 %s", groupName))
}

func (s *Session) handleList(args []string) error {

	lines := []string{
		"215 List of newsgroups follows",
		"alt.binaries.test 0 1 y",
	}
	return s.WriteMultiLine(lines)
}

func (s *Session) handleDate(args []string) error {

	now := time.Now().UTC()
	dateStr := now.Format("20060102150405")
	return s.WriteLine(fmt.Sprintf("111 %s", dateStr))
}

func (s *Session) ensureGroup(client *nntp.Client) bool {
	if s.currentGroup == "" {
		return true
	}
	if err := client.Group(s.currentGroup); err != nil {
		logger.Debug("NNTP proxy: GROUP failed on backend", "group", s.currentGroup, "err", err)
		return false
	}
	return true
}

// providerAttempt is the per-verb work the proxy runs against one backend
// connection. Returning done=true ends the provider loop (the command was
// answered); returning an error excludes that provider and tries the next.
// fatal aborts the whole session.
type providerAttempt func(client *nntp.Client, release, discard func(), providerID string) (done bool, fatal error)

// forEachProvider walks providers for one message, excluding any that fail,
// and replies "430 No such article" when none could answer. It owns the
// GetConnection / GROUP / exclude-on-error scaffolding that every article
// command needs; the verb-specific work lives in attempt.
func (s *Session) forEachProvider(verb, messageID string, attempt providerAttempt) error {
	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()

	var exclude []string
	for s.usenet != nil {
		client, release, discard, pid, err := s.usenet.GetConnection(ctx, exclude, 999, false)
		if err != nil {
			logger.Debug("NNTP proxy: GetConnection failed", "err", err)
			break
		}
		if !s.ensureGroup(client) {
			release()
			exclude = append(exclude, pid) // don't retry the same broken provider
			continue
		}
		done, fatal := attempt(client, release, discard, pid)
		if fatal != nil {
			return fatal
		}
		if done {
			return nil
		}
		exclude = append(exclude, pid) // always exclude on error; prevents infinite retry
	}

	logger.Info("NNTP proxy: "+verb+" failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}

// writeTextResponse emits a status line followed by the article text, split
// into the dot-terminated multi-line form.
func (s *Session) writeTextResponse(status, text string) error {
	lines := []string{status}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		lines = append(lines, strings.TrimSuffix(line, "\r"))
	}
	return s.WriteMultiLine(lines)
}

// recordArticleOutcome feeds the provider article-availability stats.
func (s *Session) recordArticleOutcome(pid string, err error, available bool) {
	if err != nil {
		if pool.IsArticleNotFoundError(err) {
			s.usenet.RecordProviderArticleResult(pid, false)
		}
		return
	}
	s.usenet.RecordProviderArticleResult(pid, available)
}

func (s *Session) handleArticle(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}
	messageID := normalizeMessageID(args[0])

	var replyErr error
	err := s.forEachProvider("ARTICLE", messageID, func(client *nntp.Client, release, _ func(), pid string) (bool, error) {
		article, err := client.GetArticle(messageID)
		release()
		s.recordArticleOutcome(pid, err, true)
		if err != nil {
			logger.Debug("NNTP proxy: GetArticle failed", "messageID", messageID, "err", err)
			return false, nil
		}
		replyErr = s.writeTextResponse(fmt.Sprintf("220 0 %s", messageID), article)
		return true, nil
	})
	if err != nil {
		return err
	}
	return replyErr
}

func (s *Session) handleHead(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}
	messageID := normalizeMessageID(args[0])

	var replyErr error
	err := s.forEachProvider("HEAD", messageID, func(client *nntp.Client, release, _ func(), pid string) (bool, error) {
		head, err := client.GetHead(messageID)
		release()
		s.recordArticleOutcome(pid, err, true)
		if err != nil {
			logger.Debug("NNTP proxy: GetHead failed", "messageID", messageID, "err", err)
			return false, nil
		}
		replyErr = s.writeTextResponse(fmt.Sprintf("221 0 %s", messageID), head)
		return true, nil
	})
	if err != nil {
		return err
	}
	return replyErr
}

func (s *Session) handleBody(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}
	messageID := normalizeMessageID(args[0])

	return s.forEachProvider("BODY", messageID, func(client *nntp.Client, release, discard func(), pid string) (bool, error) {
		written, err := client.StreamBody(messageID, s.conn)
		if err != nil {
			if isClientWriteError(err) {
				discard()
				return false, err
			}
			s.recordArticleOutcome(pid, err, false)
			logger.Debug("NNTP proxy: StreamBody failed", "messageID", messageID, "err", err)
			discard()
			if written > 0 {
				// Part of the 222 response already reached the client; retrying
				// on another provider would emit a second status line mid-body.
				// The only safe recovery is dropping the proxy connection so
				// the downstream client re-requests cleanly.
				s.shouldQuit = true
				return false, fmt.Errorf("BODY %s aborted after %d bytes already written to client: %w", messageID, written, err)
			}
			return false, nil
		}
		release()
		s.usenet.RecordProviderArticleResult(pid, true)
		return true, nil
	})
}

func (s *Session) handleStat(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}
	messageID := normalizeMessageID(args[0])

	return s.forEachProvider("STAT", messageID, func(client *nntp.Client, release, _ func(), pid string) (bool, error) {
		exists, err := client.CheckArticle(messageID)
		release()
		if err != nil {
			s.recordArticleOutcome(pid, err, false)
			logger.Debug("NNTP proxy: CheckArticle failed", "messageID", messageID, "err", err)
			return false, nil
		}
		s.usenet.RecordProviderArticleResult(pid, exists)
		if !exists {
			// Definitively absent here; ask the next provider.
			return false, nil
		}
		// A write failure here is fatal for the session, not a provider fault.
		return true, s.WriteLine(fmt.Sprintf("223 0 %s", messageID))
	})
}
