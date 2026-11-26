package nfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

// ONC RPC (RFC 5531) over TCP, the transport both MOUNT and NFS ride on. Each
// message is framed by record marking: a four-byte header whose high bit
// says "last fragment" and whose low 31 bits are the fragment length. A call
// carries a transaction id, the program, version and procedure wanted, and
// two authentication blocks (credential and verifier); a reply echoes the id
// and says whether the call was accepted and, if so, how the procedure went.

const (
	rpcVersion = 2

	msgCall  = 0
	msgReply = 1

	replyAccepted = 0
	replyDenied   = 1

	// accept_stat
	acceptSuccess      = 0
	acceptProgUnavail  = 1
	acceptProgMismatch = 2
	acceptProcUnavail  = 3
	acceptGarbageArgs  = 4
	acceptSystemErr    = 5

	// reject_stat
	rejectRPCMismatch = 0
	rejectAuthError   = 1

	// auth_stat for a rejected credential
	authRejectedCred = 2

	authFlavorNone = 0
	authFlavorSys  = 1

	// Credential and verifier bodies are at most 400 bytes by the RFC.
	maxAuthBody = 400

	// maxRecordBytes bounds one call. The largest legitimate call is a
	// WRITE, which a read-only server refuses anyway; a READDIRPLUS or
	// LOOKUP call is a few hundred bytes. Anything bigger is not a client.
	maxRecordBytes = 1 << 20

	// maxInFlightPerConn caps the calls one connection may have in progress.
	// Clients pipeline, and READs on a cold file take seconds, so one slow
	// file must not stop the directory listing queued behind it; but a peer
	// cannot be allowed to fan out without limit either.
	maxInFlightPerConn = 32

	replyWriteTimeout = 60 * time.Second
)

// rpcCall is one decoded call.
type rpcCall struct {
	xid  uint32
	prog uint32
	vers uint32
	proc uint32
	// cred is the credential flavor the client sent (AUTH_SYS or AUTH_NONE).
	// The export trusts neither — the stream token in the mount path is the
	// gate — so the body is parsed only far enough to skip it.
	cred uint32
	args *decoder
}

// readRecord reads one record-marked message: fragments until the last one.
// maxBytes bounds the reassembled record.
func readRecord(r io.Reader, maxBytes int) ([]byte, error) {
	var record []byte
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, err
		}
		mark := binary.BigEndian.Uint32(hdr[:])
		last := mark&0x80000000 != 0
		n := int(mark & 0x7fffffff)
		if n > maxBytes || len(record)+n > maxBytes {
			return nil, fmt.Errorf("rpc: record of %d bytes exceeds the %d byte limit", len(record)+n, maxBytes)
		}
		frag := make([]byte, n)
		if _, err := io.ReadFull(r, frag); err != nil {
			return nil, err
		}
		record = append(record, frag...)
		if last {
			return record, nil
		}
	}
}

// frameRecord prefixes a reply with a single last-fragment record mark.
func frameRecord(body []byte) []byte {
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body))|0x80000000)
	copy(out[4:], body)
	return out
}

var errNotACall = errors.New("rpc: message is not a call")

// parseCall decodes the RPC header off a record and leaves the decoder
// positioned at the procedure arguments.
func parseCall(record []byte) (*rpcCall, error) {
	d := newDecoder(record)
	c := &rpcCall{}
	c.xid = d.uint32()
	if mtype := d.uint32(); mtype != msgCall {
		return nil, errNotACall
	}
	rpcvers := d.uint32()
	c.prog = d.uint32()
	c.vers = d.uint32()
	c.proc = d.uint32()
	c.cred = d.uint32()
	d.opaque(maxAuthBody)
	d.uint32() // verifier flavor
	d.opaque(maxAuthBody)
	if err := d.err(); err != nil {
		return nil, err
	}
	if rpcvers != rpcVersion {
		return c, errRPCVersion
	}
	c.args = d
	return c, nil
}

var errRPCVersion = errors.New("rpc: unsupported RPC version")

// replyHeader starts an accepted reply: xid, REPLY, MSG_ACCEPTED, a null
// verifier and the accept status. Procedure results follow it.
func replyHeader(xid uint32, acceptStat uint32) *encoder {
	e := &encoder{buf: make([]byte, 0, 256)}
	e.uint32(xid)
	e.uint32(msgReply)
	e.uint32(replyAccepted)
	e.uint32(authFlavorNone)
	e.uint32(0)
	e.uint32(acceptStat)
	return e
}

func replyProgMismatch(xid uint32, low, high uint32) []byte {
	e := replyHeader(xid, acceptProgMismatch)
	e.uint32(low)
	e.uint32(high)
	return e.bytes()
}

// replyDeniedRPCMismatch is the one denied reply the server sends: a call
// speaking an RPC version other than 2.
func replyDeniedRPCMismatch(xid uint32) []byte {
	e := &encoder{}
	e.uint32(xid)
	e.uint32(msgReply)
	e.uint32(replyDenied)
	e.uint32(rejectRPCMismatch)
	e.uint32(rpcVersion)
	e.uint32(rpcVersion)
	return e.bytes()
}

// conn is one client TCP connection. Calls are dispatched concurrently up to
// maxInFlightPerConn; replies are written whole, one at a time, in whatever
// order they finish — RPC matches them by xid, not position.
type conn struct {
	s    *Server
	c    net.Conn
	wmu  sync.Mutex
	addr string
}

func (s *Server) serveConn(nc net.Conn) {
	c := &conn{s: s, c: nc, addr: nc.RemoteAddr().String()}
	defer nc.Close()
	logger.Trace("NFS connection opened", "client", c.addr)
	defer logger.Trace("NFS connection closed", "client", c.addr)

	inflight := make(chan struct{}, maxInFlightPerConn)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		record, err := readRecord(nc, maxRecordBytes)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isClosedConn(err) {
				logger.Debug("NFS connection read failed", "client", c.addr, "err", err)
			}
			return
		}
		select {
		case inflight <- struct{}{}:
		case <-s.stopCh:
			return
		}
		wg.Add(1)
		go func(record []byte) {
			defer wg.Done()
			defer func() { <-inflight }()
			if reply := c.dispatch(record); reply != nil {
				c.write(reply)
			}
		}(record)
	}
}

func (c *conn) write(reply []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.c.SetWriteDeadline(time.Now().Add(replyWriteTimeout))
	if _, err := c.c.Write(frameRecord(reply)); err != nil && !isClosedConn(err) {
		logger.Debug("NFS reply write failed", "client", c.addr, "err", err)
		_ = c.c.Close()
	}
}

// dispatch routes one call to its program. A reply of nil means the record
// was not a call, which RPC says to ignore.
func (c *conn) dispatch(record []byte) (reply []byte) {
	call, err := parseCall(record)
	switch {
	case errors.Is(err, errNotACall):
		return nil
	case errors.Is(err, errRPCVersion):
		return replyDeniedRPCMismatch(call.xid)
	case err != nil:
		// Too short to carry an xid worth answering to.
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			logger.Error("NFS handler panicked", "client", c.addr, "prog", call.prog, "proc", call.proc, "panic", fmt.Sprint(r))
			reply = replyHeader(call.xid, acceptSystemErr).bytes()
		}
	}()
	switch call.prog {
	case progPortmap:
		if call.vers != portmapVersion {
			return replyProgMismatch(call.xid, portmapVersion, portmapVersion)
		}
		return c.s.portmapProc(call)
	case progMount:
		if call.vers != mountVersion {
			return replyProgMismatch(call.xid, mountVersion, mountVersion)
		}
		return c.s.mountProc(c, call)
	case progNFS:
		if call.vers != nfsVersion {
			return replyProgMismatch(call.xid, nfsVersion, nfsVersion)
		}
		start := time.Now()
		reply = c.s.nfsProc(c, call)
		c.traceCall(call, reply, start)
		return reply
	default:
		return replyHeader(call.xid, acceptProgUnavail).bytes()
	}
}

// traceCall logs one NFS call and its status at Trace, which is the view a
// client-side problem is diagnosed from: which procedure it is stuck on,
// and what it was told.
func (c *conn) traceCall(call *rpcCall, reply []byte, start time.Time) {
	status := "-"
	// Reply header is 24 bytes (xid, REPLY, ACCEPTED, null verifier, SUCCESS);
	// an NFS result opens with its nfsstat3.
	if len(reply) >= 28 && binary.BigEndian.Uint32(reply[20:24]) == acceptSuccess {
		status = strconv.Itoa(int(binary.BigEndian.Uint32(reply[24:28])))
	} else if len(reply) >= 24 {
		status = "accept=" + strconv.Itoa(int(binary.BigEndian.Uint32(reply[20:24])))
	}
	logger.Trace("NFS call", "client", c.addr, "proc", nfsProcName(call.proc), "status", status, "ms", time.Since(start).Milliseconds(), "reply_bytes", len(reply))
}

func isClosedConn(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	// A peer that vanished mid-read reports a reset; neither side's bug.
	return errors.Is(err, io.ErrUnexpectedEOF)
}
