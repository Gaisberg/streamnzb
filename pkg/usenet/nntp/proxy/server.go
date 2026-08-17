package proxy

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/usenet/pool"
)

type Server struct {
	host     string
	port     int
	usenet   *pool.Pool
	authUser string
	authPass string

	listener net.Listener
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewServer(host string, port int, usenet *pool.Pool, authUser, authPass string) (*Server, error) {
	s := &Server{
		host:     host,
		port:     port,
		usenet:   usenet,
		authUser: authUser,
		authPass: authPass,
		sessions: make(map[string]*Session),
	}

	if err := s.Validate(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Server) Validate() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("NNTP proxy port %d is already in use", s.port)
	}
	ln.Close()

	return nil
}

func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start NNTP proxy: %w", err)
	}

	s.listener = listener
	logger.Info("NNTP proxy listening", "addr", addr)

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
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
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
