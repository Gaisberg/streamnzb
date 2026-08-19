package proxy

import (
	"bufio"
	"fmt"
	"net"
	"strings"

	"streamnzb/pkg/usenet/pool"
)

// clientWriteBufferSize batches response bytes headed for the downstream
// client. Article bodies arrive as ~128-byte yEnc lines, and writing each one
// straight to the socket cost a syscall (and, with TCP_NODELAY, a packet) per
// line — the dominant cost of a BODY relay by two orders of magnitude.
const clientWriteBufferSize = 64 * 1024

type Session struct {
	conn     net.Conn
	bw       *bufio.Writer
	usenet   *pool.Pool
	authUser string
	authPass string

	authenticated bool
	currentGroup  string
	shouldQuit    bool
}

func NewSession(conn net.Conn, usenet *pool.Pool, authUser, authPass string) *Session {
	return &Session{
		conn:          conn,
		bw:            bufio.NewWriterSize(conn, clientWriteBufferSize),
		usenet:        usenet,
		authUser:      authUser,
		authPass:      authPass,
		authenticated: authUser == "",
	}
}

func (s *Session) WriteLine(line string) error {
	_, err := fmt.Fprintf(s.bw, "%s\r\n", line)
	return err
}

// Flush pushes buffered response bytes to the client. NNTP is strictly
// request/response, so this must run before the session waits on the next
// command or the client blocks forever on a reply sitting in our buffer.
func (s *Session) Flush() error {
	return s.bw.Flush()
}

// buffered reports response bytes still held back from the client.
func (s *Session) buffered() int {
	return s.bw.Buffered()
}

// discardBuffered drops an unsent partial response and clears the sticky write
// error, so the session can start the reply over. Only sound while nothing of
// that response has reached the client.
func (s *Session) discardBuffered() {
	s.bw.Reset(s.conn)
}

func (s *Session) WriteMultiLine(lines []string) error {
	for _, line := range lines {

		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		if err := s.WriteLine(line); err != nil {
			return err
		}
	}

	return s.WriteLine(".")
}

func (s *Session) ShouldQuit() bool {
	return s.shouldQuit
}

func (s *Session) CurrentGroup() string {
	return s.currentGroup
}

func (s *Session) HandleCommand(cmd string, args []string) error {

	switch cmd {
	case "QUIT":
		return s.handleQuit(args)
	case "CAPABILITIES":
		return s.handleCapabilities(args)
	case "AUTHINFO":
		return s.handleAuthInfo(args)
	}

	if !s.authenticated {
		return s.WriteLine("480 Authentication required")
	}

	switch cmd {
	case "GROUP":
		return s.handleGroup(args)
	case "ARTICLE":
		return s.handleArticle(args)
	case "BODY":
		return s.handleBody(args)
	case "HEAD":
		return s.handleHead(args)
	case "STAT":
		return s.handleStat(args)
	case "LIST":
		return s.handleList(args)
	case "DATE":
		return s.handleDate(args)
	case "MODE":

		if len(args) >= 1 && strings.ToUpper(args[0]) == "READER" {
			return s.WriteLine("201 StreamNZB proxy (reader mode)")
		}
		return s.WriteLine(fmt.Sprintf("500 Unknown command: %s", cmd))
	default:
		return s.WriteLine(fmt.Sprintf("500 Unknown command: %s", cmd))
	}
}
