package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/usenet/pool"
)

// privilegedPortCeiling is the highest port a non-root process cannot bind on
// Linux. The default NNTP port (119) sits below it, so every containerised
// deployment that drops root fails there — hence the hint on bind errors.
const privilegedPortCeiling = 1024

type Server struct {
	host     string
	port     int
	usenet   *pool.Pool
	authUser string
	authPass string

	listener net.Listener
	// listening is tracked separately from the listener handle: Stop must not
	// clear the handle out from under a Serve goroutine that is still starting.
	listening bool
	mu        sync.Mutex
	sessions  map[string]*Session
	lastErr   string
}

func NewServer(host string, port int, usenet *pool.Pool, authUser, authPass string) *Server {
	return &Server{
		host:     host,
		port:     port,
		usenet:   usenet,
		authUser: authUser,
		authPass: authPass,
		sessions: make(map[string]*Session),
	}
}

func (s *Server) addr() string {
	return fmt.Sprintf("%s:%d", s.host, s.port)
}

// Listen binds the proxy port. It is separate from Serve so both startup and
// config reload learn synchronously whether the bind worked — the caller can
// report a failure instead of discovering it from a goroutine after the fact.
//
// A bind failure is never fatal to the process: the proxy is one optional
// feature, and refusing to boot the addon over it costs the user everything
// else. See issue #192.
func (s *Server) Listen() error {
	listener, err := net.Listen("tcp", s.addr())
	if err != nil {
		bindErr := s.describeBindError(err)
		s.mu.Lock()
		s.lastErr = bindErr.Error()
		s.mu.Unlock()
		return bindErr
	}

	s.mu.Lock()
	s.listener = listener
	s.listening = true
	s.lastErr = ""
	s.mu.Unlock()

	logger.Info("NNTP proxy listening", "addr", s.addr())
	return nil
}

// describeBindError turns a raw bind failure into something a user can act on.
// The two cases that actually happen are a privileged port without root and a
// port already taken by something else.
func (s *Server) describeBindError(err error) error {
	if errors.Is(err, os.ErrPermission) && s.port < privilegedPortCeiling {
		return fmt.Errorf("port %d needs root; pick a port above %d (for example 1119) and point your downloader at it", s.port, privilegedPortCeiling-1)
	}
	if isAddrInUse(err) {
		return fmt.Errorf("port %d is already in use", s.port)
	}
	return fmt.Errorf("could not bind %s: %w", s.addr(), err)
}

// isAddrInUse matches on message text rather than a syscall sentinel because
// the code differs per platform (EADDRINUSE on Unix, WSAEADDRINUSE on Windows)
// and this only decides the wording of a log line.
func isAddrInUse(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address")
}

// Serve accepts connections until Stop closes the listener. Listen must have
// succeeded first.
func (s *Server) Serve() error {
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()

	if listener == nil {
		return fmt.Errorf("NNTP proxy: Serve called before a successful Listen")
	}

	for {
		conn, err := listener.Accept()
		if err != nil {

			if strings.Contains(err.Error(), "use of closed network connection") || strings.Contains(err.Error(), "closed") {
				return nil
			}
			logger.Error("NNTP proxy accept error", "err", err)
			continue
		}

		go s.handleConnection(conn)
	}
}

func (s *Server) Stop() error {
	s.mu.Lock()
	listener := s.listener
	s.listening = false
	s.mu.Unlock()

	if listener != nil {
		return listener.Close()
	}
	return nil
}

// Status describes the listener for the dashboard, so a proxy that is enabled
// but not actually listening says so instead of looking healthy.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Listening bool   `json:"listening"`
	Addr      string `json:"addr"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	return Status{
		Enabled:   true,
		Listening: s.listening,
		Addr:      s.addr(),
		Error:     s.lastErr,
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	session := NewSession(conn, s.usenet, s.authUser, s.authPass)

	s.mu.Lock()
	s.sessions[conn.RemoteAddr().String()] = session
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.sessions, conn.RemoteAddr().String())
		s.mu.Unlock()
	}()

	session.WriteLine("201 StreamNZB NNTP Proxy ready (posting prohibited)")
	if err := session.Flush(); err != nil {
		return
	}

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		cmd := strings.ToUpper(parts[0])
		args := parts[1:]

		err := session.HandleCommand(cmd, args)
		if err != nil {
			logger.Error("NNTP proxy command error", "remote", conn.RemoteAddr(), "cmd", cmd, "err", err)
			// A handler that aborts mid-response (e.g. after a partial BODY
			// stream) sets ShouldQuit; injecting a 500 line into that stream
			// would corrupt it further, so only respond on intact sessions.
			if !session.ShouldQuit() {
				_ = session.WriteLine(fmt.Sprintf("500 %v", err))
			}
		}

		// Responses are batched in the session write buffer; nothing reaches
		// the client until this flush, so it must happen before the session
		// blocks on the next command (and before a QUIT closes the socket).
		if err := session.Flush(); err != nil {
			logger.Debug("NNTP proxy: flush to client failed", "remote", conn.RemoteAddr(), "cmd", cmd, "err", err)
			return
		}

		if session.ShouldQuit() {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Error("NNTP proxy scanner error", "remote", conn.RemoteAddr(), "err", err)
	}
}

type ProxySessionInfo struct {
	ID           string `json:"id"`
	RemoteAddr   string `json:"remote_addr"`
	CurrentGroup string `json:"current_group"`
}

func (s *Server) GetSessions() []ProxySessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var list []ProxySessionInfo
	for id, session := range s.sessions {
		list = append(list, ProxySessionInfo{
			ID:           id,
			RemoteAddr:   id,
			CurrentGroup: session.CurrentGroup(),
		})
	}
	return list
}
