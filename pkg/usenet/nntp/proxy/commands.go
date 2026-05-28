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
		"POST",
		"IHAVE",
		"STREAMING",
	}

	if s.authUser != "" {
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

func (s *Session) handleArticle(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	messageID := normalizeMessageID(args[0])
	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()

	var exclude []string
	for {
		if s.usenet == nil {
			break
		}
		client, release, _, pid, err := s.usenet.GetConnection(ctx, exclude, 999, false)
		if err != nil {
			logger.Debug("NNTP proxy: GetConnection failed", "err", err)
			break
		}
		if !s.ensureGroup(client) {
			release()
			exclude = append(exclude, pid) // don't retry the same broken provider
			continue
		}
		article, err := client.GetArticle(messageID)
		release()
		if err != nil {
			if pool.IsArticleNotFoundError(err) {
				s.usenet.RecordProviderArticleResult(pid, false)
			}
			logger.Debug("NNTP proxy: GetArticle failed", "messageID", messageID, "err", err)
			exclude = append(exclude, pid) // always exclude on error; prevents infinite retry
			continue
		}
		s.usenet.RecordProviderArticleResult(pid, true)
		lines := []string{fmt.Sprintf("220 0 %s", messageID)}
		for _, line := range strings.Split(strings.ReplaceAll(article, "\r\n", "\n"), "\n") {
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}
		return s.WriteMultiLine(lines)
	}

	logger.Info("NNTP proxy: ARTICLE failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}

func (s *Session) handleBody(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	messageID := normalizeMessageID(args[0])
	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()

	var exclude []string
	for {
		if s.usenet == nil {
			break
		}
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
		_, err = client.StreamBody(messageID, s.conn)
		if err != nil {
			if isClientWriteError(err) {
				discard()
				return err
			}
			if pool.IsArticleNotFoundError(err) {
				s.usenet.RecordProviderArticleResult(pid, false)
			}
			logger.Debug("NNTP proxy: StreamBody failed", "messageID", messageID, "err", err)
			discard()
			exclude = append(exclude, pid) // always exclude on error; prevents infinite retry
			continue
		}
		release()
		s.usenet.RecordProviderArticleResult(pid, true)
		return nil
	}

	logger.Info("NNTP proxy: BODY failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}

func (s *Session) handleHead(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	messageID := normalizeMessageID(args[0])
	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()

	var exclude []string
	for {
		if s.usenet == nil {
			break
		}
		client, release, _, pid, err := s.usenet.GetConnection(ctx, exclude, 999, false)
		if err != nil {
			logger.Debug("NNTP proxy: GetConnection failed", "err", err)
			break
		}
		if !s.ensureGroup(client) {
			release()
			exclude = append(exclude, pid) // don't retry the same broken provider
			continue
		}
		head, err := client.GetHead(messageID)
		release()
		if err != nil {
			if pool.IsArticleNotFoundError(err) {
				s.usenet.RecordProviderArticleResult(pid, false)
			}
			logger.Debug("NNTP proxy: GetHead failed", "messageID", messageID, "err", err)
			exclude = append(exclude, pid) // always exclude on error; prevents infinite retry
			continue
		}
		s.usenet.RecordProviderArticleResult(pid, true)
		lines := []string{fmt.Sprintf("221 0 %s", messageID)}
		for _, line := range strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n") {
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}
		return s.WriteMultiLine(lines)
	}

	logger.Info("NNTP proxy: HEAD failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}

func (s *Session) handleStat(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	messageID := normalizeMessageID(args[0])
	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()

	var exclude []string
	for {
		if s.usenet == nil {
			break
		}
		client, release, _, pid, err := s.usenet.GetConnection(ctx, exclude, 999, false)
		if err != nil {
			logger.Debug("NNTP proxy: GetConnection failed", "err", err)
			break
		}
		if !s.ensureGroup(client) {
			release()
			exclude = append(exclude, pid) // don't retry the same broken provider
			continue
		}
		exists, err := client.CheckArticle(messageID)
		release()
		if err != nil {
			if pool.IsArticleNotFoundError(err) {
				s.usenet.RecordProviderArticleResult(pid, false)
			}
			logger.Debug("NNTP proxy: CheckArticle failed", "messageID", messageID, "err", err)
			exclude = append(exclude, pid) // always exclude on error; prevents infinite retry
			continue
		}
		if exists {
			s.usenet.RecordProviderArticleResult(pid, true)
			return s.WriteLine(fmt.Sprintf("223 0 %s", messageID))
		}
		s.usenet.RecordProviderArticleResult(pid, false)
		exclude = append(exclude, pid)
	}

	logger.Info("NNTP proxy: STAT failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}
