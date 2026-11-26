// Package nfs serves the release library as a read-only NFSv3 export, so a
// media server that wants a folder of files — Plex first among them — can
// mount it and scan it like any other share.
//
// It is a protocol translation, not a second store: the tree is built from
// the library's validated rows, and a read is the playback byte stream a
// /play request would serve, through the same stream's providers and caps.
// MOUNT and NFS both answer on one TCP port; a portmapper on 111 is tried
// for clients that cannot be told a port (Windows), and otherwise a
// client names the port in its mount options.
//
// The stream token in the mount path is the only credential. NFSv3's own
// authentication trusts whatever uid the client claims, so the listener
// belongs on a LAN or VPN address; see docs/nfs.md.
package nfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

const (
	progMount    = 100005
	mountVersion = 3
	progNFS      = 100003
	nfsVersion   = 3

	// treeTTL is how long a built tree is served before the library is read
	// again. Rows are added by plays, so seconds is fine; a client's own
	// attribute cache is of the same order.
	treeTTL = 30 * time.Second

	libraryPageSize = 200
)

// Streams is the slice of the stream manager the export needs.
type Streams interface {
	AuthenticateToken(token, adminUsername, adminToken string) (*auth.Stream, error)
	GetAllStreams() []auth.Stream
}

// Library is the slice of the library store the export reads.
// *persistence.LibraryStore satisfies it.
type Library interface {
	GetFilteredItems(search, contentType string, pinnedOnly bool, statusFilter string, offset, limit int) ([]*persistence.LibraryItem, int, error)
}

// OpenFunc opens one library row as a seekable byte stream for a stream,
// returning the stream and its exact size. clientAddr names the reader for
// the dashboard. *stremio.Server's OpenLibraryFile is the implementation.
type OpenFunc func(ctx context.Context, stream *auth.Stream, item *persistence.LibraryItem, clientAddr string) (io.ReadSeekCloser, int64, error)

type Options struct {
	// Host is the listen address; empty means every interface.
	Host string
	Port int

	Streams Streams
	// Admin returns the dashboard username and admin token, so the admin
	// identity can be refused: it binds no providers and owns no connection
	// budget, which is what every read is charged to.
	Admin func() (username, token string)

	Library Library
	Open    OpenFunc
	// Titles names titles for the generated library folder. Nil leaves it
	// naming by parsed release names.
	Titles TitleFunc
	// Export is the client-owned tree: downloads/ and whatever else it makes.
	Export Export
}

type Server struct {
	opts Options

	mu        sync.Mutex
	listener  net.Listener
	listening bool
	lastErr   string
	conns     map[net.Conn]struct{}
	pmapTCP   net.Listener
	pmapUDP   net.PacketConn
	pmapErr   string

	stopOnce sync.Once
	stopCh   chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	treeMu sync.Mutex
	tree   *tree
	dirty  bool
	// sizes are media sizes learned by opening files whose rows lacked one,
	// keyed by row id, so a listing is right before the row is re-read.
	sizes map[string]int64

	titles *titleCache
	files  *fileCache
}

func New(opts Options) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		opts:   opts,
		conns:  make(map[net.Conn]struct{}),
		stopCh: make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
		files:  newFileCache(),
		sizes:  make(map[string]int64),
	}
	s.titles = newTitleCache(opts.Titles, s.markDirty)
	return s
}

func (s *Server) addr() string {
	return net.JoinHostPort(strings.TrimSpace(s.opts.Host), strconv.Itoa(s.opts.Port))
}

// Addr is the address the export is listening on, or "" before Listen.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Listen binds the port. Separate from Serve so a failed bind is reported at
// startup rather than from a goroutine.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.addr())
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("NFS export: listen on %s: %w", s.addr(), err)
	}
	s.mu.Lock()
	s.listener = ln
	s.listening = true
	s.lastErr = ""
	s.mu.Unlock()
	logger.Info("NFS export listening", "addr", ln.Addr().String())
	s.listenPortmapper()

	go s.currentTree()
	s.wg.Add(1)
	go s.reaper()
	return nil
}

// Serve accepts connections until Stop. It returns nil once stopped.
func (s *Server) Serve() error {
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln == nil {
		return errors.New("NFS export: Serve called before a successful Listen")
	}
	for {
		nc, err := ln.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return nil
			default:
			}
			if isClosedConn(err) {
				return nil
			}
			logger.Error("NFS export accept failed", "err", err)
			continue
		}
		if tc, ok := nc.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetKeepAlive(true)
		}
		s.mu.Lock()
		s.conns[nc] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(nc)
			s.mu.Lock()
			delete(s.conns, nc)
			s.mu.Unlock()
		}()
	}
}

// Stop closes the listener and every client connection, then every open
// file, which ends their plays. Safe to call more than once.
func (s *Server) Stop() error {
	var err error
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.cancel()
		s.mu.Lock()
		if s.listener != nil {
			err = s.listener.Close()
		}
		if s.pmapTCP != nil {
			_ = s.pmapTCP.Close()
		}
		if s.pmapUDP != nil {
			_ = s.pmapUDP.Close()
		}
		s.listening = false
		for nc := range s.conns {
			_ = nc.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		s.files.closeAll()
	})
	return err
}

// Status is what the settings page shows for an enabled export: whether the
// listener actually came up, where, and if not, why.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Listening bool   `json:"listening"`
	Addr      string `json:"addr"`
	Error     string `json:"error,omitempty"`
	// Portmapper reports the 111 listener: bound, and if not, why. A Linux
	// host that drops root cannot bind 111; only Windows clients need it.
	Portmapper PortmapperStatus `json:"portmapper"`
}

type PortmapperStatus struct {
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Enabled: true, Listening: s.listening, Addr: s.addr(), Error: s.lastErr}
	if s.listener != nil {
		st.Addr = s.listener.Addr().String()
	}
	st.Portmapper = PortmapperStatus{Listening: s.pmapUDP != nil, Error: s.pmapErr}
	return st
}

func (s *Server) reaper() {
	defer s.wg.Done()
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			if n := s.files.reap(now); n > 0 {
				logger.Debug("NFS export closed idle files", "count", n)
			}
		}
	}
}

// --- streams -----------------------------------------------------------

func (s *Server) adminIdentity() (string, string) {
	if s.opts.Admin == nil {
		return "", ""
	}
	return s.opts.Admin()
}

var errMountRefused = errors.New("mount refused")

// authenticateMount resolves a MOUNT path to its stream. The path is the
// token, with or without slashes around it.
func (s *Server) authenticateMount(dirpath string) (*auth.Stream, error) {
	token := strings.Trim(strings.TrimSpace(dirpath), "/")
	if token == "" || strings.Contains(token, "/") || s.opts.Streams == nil {
		return nil, errMountRefused
	}
	adminUser, adminToken := s.adminIdentity()
	stream, err := s.opts.Streams.AuthenticateToken(token, adminUser, adminToken)
	if err != nil || stream == nil {
		return nil, errMountRefused
	}
	if (adminUser != "" && strings.EqualFold(stream.Username, adminUser)) || (adminToken != "" && token == adminToken) {
		return nil, fmt.Errorf("%w: the admin identity binds no providers; mount with a stream token", errMountRefused)
	}
	return stream, nil
}

// streamForKey finds the stream a handle was minted for. Looked up fresh
// each time: a stream's provider selection and caps are read off the
// returned value, and those may have changed since the mount.
func (s *Server) streamForKey(k streamKey) *auth.Stream {
	if s.opts.Streams == nil {
		return nil
	}
	for _, st := range s.opts.Streams.GetAllStreams() {
		if st.Token != "" && streamKeyFor(st.Token) == k {
			stream := st
			return &stream
		}
	}
	return nil
}

// --- tree --------------------------------------------------------------

func (s *Server) markDirty() {
	s.treeMu.Lock()
	s.dirty = true
	s.treeMu.Unlock()
}

// currentTree returns the tree, rebuilding it when stale, after a write, or
// when a title resolved since the last build. Rebuilds are cheap — one
// library read, one export-store read and cached titles — so they happen
// inline on the call that notices.
func (s *Server) currentTree() *tree {
	s.treeMu.Lock()
	defer s.treeMu.Unlock()
	now := time.Now()
	if s.tree != nil && !s.dirty && now.Sub(s.tree.built) < treeTTL {
		return s.tree
	}
	items := s.loadItems()
	for _, item := range items {
		if item != nil && item.MediaFileSize <= 0 {
			if size, ok := s.sizes[item.ID]; ok {
				item.MediaFileSize = size
			}
		}
	}
	var vfs []persistence.ExportNode
	if s.opts.Export != nil {
		if err := s.opts.Export.EnsureRoots(); err != nil {
			logger.Warn("NFS export could not create its root folders", "err", err)
		}
		if nodes, err := s.opts.Export.Nodes(); err != nil {
			logger.Warn("NFS export could not read its writable tree", "err", err)
		} else {
			vfs = nodes
		}
	}
	s.tree = buildTree(items, vfs, s.titles.cached, now)
	s.dirty = false
	s.titles.resolveMissing(s.ctx, items)
	return s.tree
}

// noteSize records a media size learned by opening the file, when the row
// did not carry one, and rebuilds the listing so GETATTR reports it. The
// opener persists it on the row as well; this covers the listing until the
// row is read again.
func (s *Server) noteSize(itemID string, size int64) {
	if itemID == "" || size <= 0 {
		return
	}
	s.treeMu.Lock()
	changed := s.sizes[itemID] != size
	if changed {
		s.sizes[itemID] = size
		s.dirty = true
	}
	s.treeMu.Unlock()
	if changed {
		logger.Debug("NFS export learned a media size", "item", itemID, "size", size)
	}
}

// loadItems reads every playable row. The store pages at 200; the export
// wants them all, so it walks the pages.
func (s *Server) loadItems() []*persistence.LibraryItem {
	if s.opts.Library == nil {
		return nil
	}
	var all []*persistence.LibraryItem
	for offset := 0; ; offset += libraryPageSize {
		page, _, err := s.opts.Library.GetFilteredItems("", "all", false, persistence.LibraryStatusGood, offset, libraryPageSize)
		if err != nil {
			logger.Warn("NFS export could not read the library", "err", err)
			break
		}
		all = append(all, page...)
		if len(page) < libraryPageSize {
			break
		}
	}
	return all
}
