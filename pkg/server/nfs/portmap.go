package nfs

import (
	"net"
	"strconv"
	"strings"

	"streamnzb/pkg/core/logger"
)

// Portmapper (RFC 1833, program 100000 version 2). A client that was not told
// a port asks it where MOUNT and NFS live; the answer is our own port. It
// exists for the Windows NFS client, which has no port option and asks on
// 111 over UDP before it does anything else. Only the two programs served
// here are ever reported, and the mapping is read-only: SET and UNSET are
// refused, so nothing can register itself through it.

// portmapPort is fixed: a client that could be told another port would not
// need a portmapper. A variable only so tests can bind an ephemeral one.
var portmapPort = 111

const (
	progPortmap    = 100000
	portmapVersion = 2

	pmapProcNull    = 0
	pmapProcSet     = 1
	pmapProcUnset   = 2
	pmapProcGetport = 3
	pmapProcDump    = 4
	pmapProcCallit  = 5

	ipprotoTCP = 6
	ipprotoUDP = 17

	// maxDatagram is the largest UDP request read. A portmapper call is a
	// few dozen bytes.
	maxDatagram = 8192
)

// portmapProc answers portmapper calls. port is the TCP port MOUNT and NFS
// are served on.
func (s *Server) portmapProc(call *rpcCall) []byte {
	port := uint32(s.servedPort())
	switch call.proc {
	case pmapProcNull:
		return replyHeader(call.xid, acceptSuccess).bytes()

	case pmapProcGetport:
		prog := call.args.uint32()
		vers := call.args.uint32()
		prot := call.args.uint32()
		call.args.uint32() // port, ignored on a query
		if call.args.err() != nil {
			return replyHeader(call.xid, acceptGarbageArgs).bytes()
		}
		e := replyHeader(call.xid, acceptSuccess)
		switch {
		case prot != ipprotoTCP:
			e.uint32(0) // nothing is served over UDP but this program itself
		case prog == progMount && (vers == 0 || vers == mountVersion),
			prog == progNFS && (vers == 0 || vers == nfsVersion):
			e.uint32(port)
		default:
			e.uint32(0)
		}
		return e.bytes()

	case pmapProcDump:
		e := replyHeader(call.xid, acceptSuccess)
		for _, m := range [][2]uint32{{progMount, mountVersion}, {progNFS, nfsVersion}} {
			e.bool(true)
			e.uint32(m[0])
			e.uint32(m[1])
			e.uint32(ipprotoTCP)
			e.uint32(port)
		}
		e.bool(false)
		return e.bytes()

	case pmapProcSet, pmapProcUnset:
		// Refused rather than PROC_UNAVAIL: the procedure exists, the
		// answer is "no" (a bool false result).
		e := replyHeader(call.xid, acceptSuccess)
		e.bool(false)
		return e.bytes()

	case pmapProcCallit:
		// Indirect calls would make this a relay; refuse the procedure.
		return replyHeader(call.xid, acceptProcUnavail).bytes()
	}
	return replyHeader(call.xid, acceptProcUnavail).bytes()
}

// servedPort is the port MOUNT and NFS answer on: the bound one when
// listening, else the configured one.
func (s *Server) servedPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		if tcp, ok := s.listener.Addr().(*net.TCPAddr); ok {
			return tcp.Port
		}
	}
	return s.opts.Port
}

// listenPortmapper binds 111 on TCP and UDP, always attempted: there is no
// switch, because the only client that needs it (Windows) cannot say so and
// the only cost is the port. A failure is recorded for the status and does
// not stop the export itself: a Linux client told its ports needs no
// portmapper, and 111 is privileged on Linux, so a container that drops root
// simply reports it unbound.
func (s *Server) listenPortmapper() {
	host := strings.TrimSpace(s.opts.Host)
	addr := net.JoinHostPort(host, strconv.Itoa(portmapPort))
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Lock()
		s.pmapErr = err.Error()
		s.mu.Unlock()
		logger.Info("NFS portmapper is not listening; Windows clients cannot mount, Linux and macOS are unaffected", "addr", addr, "err", err)
		return
	}
	udpConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		_ = tcpLn.Close()
		s.mu.Lock()
		s.pmapErr = err.Error()
		s.mu.Unlock()
		logger.Info("NFS portmapper is not listening; Windows clients cannot mount, Linux and macOS are unaffected", "addr", addr, "err", err)
		return
	}
	s.mu.Lock()
	s.pmapTCP = tcpLn
	s.pmapUDP = udpConn
	s.pmapErr = ""
	s.mu.Unlock()
	logger.Info("NFS portmapper listening", "addr", addr)

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		for {
			nc, err := tcpLn.Accept()
			if err != nil {
				return
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
	}()
	go func() {
		defer s.wg.Done()
		buf := make([]byte, maxDatagram)
		for {
			n, from, err := udpConn.ReadFrom(buf)
			if err != nil {
				return
			}
			c := &conn{s: s, addr: from.String()}
			if reply := c.dispatch(append([]byte(nil), buf[:n]...)); reply != nil {
				_, _ = udpConn.WriteTo(reply, from)
			}
		}
	}()
}
