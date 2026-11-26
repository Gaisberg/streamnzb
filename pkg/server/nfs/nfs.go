package nfs

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"streamnzb/pkg/core/logger"
)

// NFS version 3 (RFC 1813). Read-only: every procedure that would change
// something answers NFS3ERR_ROFS with the attribute shapes the RFC says a
// failure carries, so a client that tries gets a clean EROFS rather than a
// protocol error.

const (
	nfsProcNull        = 0
	nfsProcGetattr     = 1
	nfsProcSetattr     = 2
	nfsProcLookup      = 3
	nfsProcAccess      = 4
	nfsProcReadlink    = 5
	nfsProcRead        = 6
	nfsProcWrite       = 7
	nfsProcCreate      = 8
	nfsProcMkdir       = 9
	nfsProcSymlink     = 10
	nfsProcMknod       = 11
	nfsProcRemove      = 12
	nfsProcRmdir       = 13
	nfsProcRename      = 14
	nfsProcLink        = 15
	nfsProcReaddir     = 16
	nfsProcReaddirplus = 17
	nfsProcFsstat      = 18
	nfsProcFsinfo      = 19
	nfsProcPathconf    = 20
	nfsProcCommit      = 21

	nfs3OK             = 0
	nfs3ErrNoEnt       = 2
	nfs3ErrIO          = 5
	nfs3ErrAcces       = 13
	nfs3ErrNotDir      = 20
	nfs3ErrXDev        = 18
	nfs3ErrFBig        = 27
	nfs3ErrIsDir       = 21
	nfs3ErrExist       = 17
	nfs3ErrNotEmpty    = 66
	nfs3ErrInval       = 22
	nfs3ErrROFS        = 30
	nfs3ErrNameTooLong = 63
	nfs3ErrStale       = 70
	nfs3ErrBadHandle   = 10001
	nfs3ErrBadCookie   = 10003
	nfs3ErrNotSupp     = 10004
	nfs3ErrTooSmall    = 10005
	nfs3ErrServerFault = 10006
	nfs3ErrJukebox     = 10008

	nf3Reg = 1
	nf3Dir = 2

	// ACCESS bits
	access3Read    = 0x0001
	access3Lookup  = 0x0002
	access3Modify  = 0x0004
	access3Extend  = 0x0008
	access3Delete  = 0x0010
	access3Execute = 0x0020

	// freeSpaceBytes is the free space the export reports. Nothing is
	// stored, so there is no real number; a client's import refuses when it
	// thinks the disk is full, so the answer is "plenty".
	freeSpaceBytes = 1 << 50

	// FSINFO properties: files are homogeneous; no links, no symlinks, no
	// settable times.
	fsf3Homogeneous = 0x0008

	// fsid is arbitrary but constant: the client keys its inode cache on
	// (fsid, fileid).
	fsid = 0x5a4e5342 // "ZNSB"

	// maxReadBytes is rtmax: the largest READ answered. The segment loader
	// works in articles of roughly 700 KB, so 1 MiB keeps a read to one or
	// two of them.
	maxReadBytes = 1 << 20
	// dirReadPref is dtpref, the READDIR size the server suggests.
	dirReadPref = 64 << 10

	maxFileName = 4096

	// fattr3Bytes is the encoded size of one fattr3, for READDIR budgeting.
	fattr3Bytes = 84
)

var nfsProcNames = map[uint32]string{
	nfsProcNull: "NULL", nfsProcGetattr: "GETATTR", nfsProcSetattr: "SETATTR", nfsProcLookup: "LOOKUP",
	nfsProcAccess: "ACCESS", nfsProcReadlink: "READLINK", nfsProcRead: "READ", nfsProcWrite: "WRITE",
	nfsProcCreate: "CREATE", nfsProcMkdir: "MKDIR", nfsProcSymlink: "SYMLINK", nfsProcMknod: "MKNOD",
	nfsProcRemove: "REMOVE", nfsProcRmdir: "RMDIR", nfsProcRename: "RENAME", nfsProcLink: "LINK",
	nfsProcReaddir: "READDIR", nfsProcReaddirplus: "READDIRPLUS", nfsProcFsstat: "FSSTAT",
	nfsProcFsinfo: "FSINFO", nfsProcPathconf: "PATHCONF", nfsProcCommit: "COMMIT",
}

func nfsProcName(proc uint32) string {
	if name, ok := nfsProcNames[proc]; ok {
		return name
	}
	return "proc" + strconv.Itoa(int(proc))
}

// resolved is a handle the server recognised, with the node it names.
type resolved struct {
	h    handle
	node *node
	tree *tree
}

// resolveHandle decodes a handle off the arguments and finds its node.
// status is nfs3OK on success; otherwise the error the reply should carry.
func (s *Server) resolveHandle(d *decoder) (resolved, uint32) {
	raw := d.opaque(64)
	if d.err() != nil {
		return resolved{}, nfs3ErrBadHandle
	}
	h, err := decodeHandle(raw)
	if err != nil {
		return resolved{}, nfs3ErrBadHandle
	}
	t := s.currentTree()
	n := t.lookup(h.node)
	if n == nil {
		return resolved{}, nfs3ErrStale
	}
	if (n.kind == kindFile) != (h.kind == kindFile) {
		return resolved{}, nfs3ErrStale
	}
	return resolved{h: h, node: n, tree: t}, nfs3OK
}

func (r resolved) handleFor(n *node) handle {
	return handle{kind: n.kind, stream: r.h.stream, node: n.id}
}

func writeTime(e *encoder, t time.Time) {
	if t.IsZero() {
		e.uint32(0)
		e.uint32(0)
		return
	}
	e.uint32(uint32(t.Unix()))
	e.uint32(uint32(t.Nanosecond()))
}

// writeFattr encodes a node's fattr3.
func writeFattr(e *encoder, n *node) {
	if n.isDir() {
		e.uint32(nf3Dir)
		if n.generated() {
			e.uint32(0o555)
		} else {
			e.uint32(0o777)
		}
		subdirs := uint32(2)
		for _, c := range n.children {
			if c.isDir() {
				subdirs++
			}
		}
		e.uint32(subdirs)
	} else {
		e.uint32(nf3Reg)
		if n.scratch != nil {
			e.uint32(0o666)
		} else {
			e.uint32(0o444)
		}
		e.uint32(1)
	}
	e.uint32(0) // uid
	e.uint32(0) // gid
	size := uint64(n.size)
	if n.isDir() {
		size = 4096
	}
	e.uint64(size)
	e.uint64(size) // used
	e.uint32(0)    // rdev major
	e.uint32(0)    // rdev minor
	e.uint64(fsid)
	e.uint64(n.id.fileID())
	writeTime(e, n.mtime) // atime
	writeTime(e, n.mtime) // mtime
	writeTime(e, n.mtime) // ctime
}

// writePostOpAttr encodes a post_op_attr: present when n is known.
func writePostOpAttr(e *encoder, n *node) {
	if n == nil {
		e.bool(false)
		return
	}
	e.bool(true)
	writeFattr(e, n)
}

// writeWccData encodes a wcc_data with no before-attributes.
func writeWccData(e *encoder, n *node) {
	e.bool(false)
	writePostOpAttr(e, n)
}

// failWithAttr is the common failure reply: status, then a post_op_attr for
// the object when it was resolved.
func failWithAttr(xid, status uint32, n *node) []byte {
	e := replyHeader(xid, acceptSuccess)
	e.uint32(status)
	writePostOpAttr(e, n)
	return e.bytes()
}

func (s *Server) nfsProc(c *conn, call *rpcCall) []byte {
	xid := call.xid
	args := call.args
	switch call.proc {
	case nfsProcNull:
		return replyHeader(xid, acceptSuccess).bytes()

	case nfsProcGetattr:
		r, st := s.resolveHandle(args)
		if st != nfs3OK {
			return failStatus(xid, st)
		}
		e := replyHeader(xid, acceptSuccess)
		e.uint32(nfs3OK)
		writeFattr(e, s.withKnownSize(c, r, r.node))
		return e.bytes()

	case nfsProcSetattr:
		return s.setattr(xid, args)

	case nfsProcLookup:
		return s.lookup(c, xid, args)

	case nfsProcAccess:
		r, st := s.resolveHandle(args)
		wanted := args.uint32()
		if st != nfs3OK {
			return failWithAttr(xid, st, nil)
		}
		allowed := uint32(access3Read)
		if r.node.isDir() {
			allowed |= access3Lookup | access3Execute
			if !r.node.generated() {
				allowed |= access3Modify | access3Extend | access3Delete
			}
		}
		e := replyHeader(xid, acceptSuccess)
		e.uint32(nfs3OK)
		writePostOpAttr(e, r.node)
		e.uint32(wanted & allowed)
		return e.bytes()

	case nfsProcReadlink:
		r, st := s.resolveHandle(args)
		if st != nfs3OK {
			return failWithAttr(xid, st, nil)
		}
		return failWithAttr(xid, nfs3ErrInval, r.node)

	case nfsProcRead:
		return s.read(c, xid, args)

	case nfsProcWrite:
		return s.write(xid, args)

	case nfsProcCommit:
		return s.commit(xid, args)

	case nfsProcMkdir:
		return s.mkdir(xid, args)

	case nfsProcRemove:
		return s.remove(xid, args, false)

	case nfsProcRmdir:
		return s.remove(xid, args, true)

	case nfsProcRename:
		return s.rename(xid, args)

	case nfsProcCreate:
		return s.create(xid, args)

	case nfsProcSymlink, nfsProcMknod:
		r, _ := s.resolveHandle(args)
		e := replyHeader(xid, acceptSuccess)
		e.uint32(nfs3ErrROFS)
		writeWccData(e, r.node)
		return e.bytes()

	case nfsProcLink:
		file, _ := s.resolveHandle(args)
		dir, _ := s.resolveHandle(args)
		e := replyHeader(xid, acceptSuccess)
		e.uint32(nfs3ErrROFS)
		writePostOpAttr(e, file.node)
		writeWccData(e, dir.node)
		return e.bytes()

	case nfsProcReaddir:
		return s.readdir(xid, args, false)

	case nfsProcReaddirplus:
		return s.readdir(xid, args, true)

	case nfsProcFsstat:
		r, st := s.resolveHandle(args)
		if st != nfs3OK {
			return failWithAttr(xid, st, nil)
		}
		var bytes, files uint64
		for _, n := range r.tree.nodes {
			if !n.isDir() {
				bytes += uint64(n.size)
				files++
			}
		}
		e := replyHeader(xid, acceptSuccess)
		e.uint32(nfs3OK)
		writePostOpAttr(e, r.node)
		e.uint64(bytes + freeSpaceBytes) // tbytes
		e.uint64(freeSpaceBytes)         // fbytes
		e.uint64(freeSpaceBytes)         // abytes
		e.uint64(files)                  // tfiles
		e.uint64(1 << 30)                // ffiles
		e.uint64(1 << 30)                // afiles
		e.uint32(0)                      // invarsec
		return e.bytes()

	case nfsProcFsinfo:
		r, st := s.resolveHandle(args)
		if st != nfs3OK {
			return failWithAttr(xid, st, nil)
		}
		e := replyHeader(xid, acceptSuccess)
		e.uint32(nfs3OK)
		writePostOpAttr(e, r.node)
		e.uint32(maxReadBytes) // rtmax
		e.uint32(maxReadBytes) // rtpref
		e.uint32(4096)         // rtmult
		e.uint32(maxReadBytes) // wtmax — never honoured, but a zero here upsets clients
		e.uint32(maxReadBytes) // wtpref
		e.uint32(4096)         // wtmult
		e.uint32(dirReadPref)  // dtpref
		e.uint64(1<<63 - 1)    // maxfilesize
		e.uint32(0)            // time_delta seconds
		e.uint32(1)            // time_delta nseconds
		e.uint32(fsf3Homogeneous)
		return e.bytes()

	case nfsProcPathconf:
		r, st := s.resolveHandle(args)
		if st != nfs3OK {
			return failWithAttr(xid, st, nil)
		}
		e := replyHeader(xid, acceptSuccess)
		e.uint32(nfs3OK)
		writePostOpAttr(e, r.node)
		e.uint32(1)   // linkmax
		e.uint32(255) // name_max
		e.bool(true)  // no_trunc
		e.bool(true)  // chown_restricted
		e.bool(false) // case_insensitive
		e.bool(true)  // case_preserving
		return e.bytes()
	}
	return replyHeader(xid, acceptProcUnavail).bytes()
}

// failStatus is a bare failure: status and nothing else (GETATTR).
func failStatus(xid, status uint32) []byte {
	e := replyHeader(xid, acceptSuccess)
	e.uint32(status)
	return e.bytes()
}

func (s *Server) lookup(c *conn, xid uint32, args *decoder) []byte {
	r, st := s.resolveHandle(args)
	name := args.string(maxFileName)
	if args.err() != nil {
		return replyHeader(xid, acceptGarbageArgs).bytes()
	}
	if st != nfs3OK {
		return failWithAttr(xid, st, nil)
	}
	dir := r.node
	if !dir.isDir() {
		return failWithAttr(xid, nfs3ErrNotDir, dir)
	}
	var target *node
	switch name {
	case ".":
		target = dir
	case "..":
		target = dir.parent
		if target == nil {
			target = dir
		}
	default:
		if len(name) > maxNameBytes+16 {
			return failWithAttr(xid, nfs3ErrNameTooLong, dir)
		}
		target = dir.child(name)
	}
	if target == nil {
		return failWithAttr(xid, nfs3ErrNoEnt, dir)
	}
	target = s.withKnownSize(c, r, target)
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	e.opaque(r.handleFor(target).encode())
	writePostOpAttr(e, target)
	writePostOpAttr(e, dir)
	return e.bytes()
}

// withKnownSize makes sure a file's reported size is the media file's, not
// the release's. Rows stored before the size was recorded only have the
// latter, which is larger: a player told it seeks past the real end for the
// container's index and finds nothing. For such a row the file is opened
// here — a LOOKUP by name is a player about to read it — which measures the
// size, records it, and refreshes the listing; the node returned is the
// refreshed one. Rows that know their size cost nothing.
func (s *Server) withKnownSize(c *conn, r resolved, n *node) *node {
	if n == nil || n.isDir() || n.item == nil || n.scratch != nil || n.item.MediaFileSize > 0 || s.opts.Open == nil {
		return n
	}
	stream := s.streamForKey(r.h.stream)
	if stream == nil {
		return n
	}
	item := n.item
	open := func(ctx context.Context) (io.ReadSeekCloser, int64, error) {
		rs, size, err := s.opts.Open(ctx, stream, item, c.addr)
		if err == nil && size > 0 {
			s.noteSize(item.ID, size)
		}
		return rs, size, err
	}
	// A zero-length read: opens (or finds open) the file, reads nothing.
	if _, _, err := s.files.readAt(s.ctx, openKey{stream: r.h.stream, node: n.id}, open, 0, 0); err != nil {
		logger.Debug("NFS could not measure a legacy row's media size", "file", n.name, "err", err)
		return n
	}
	if fresh := s.currentTree().lookup(n.id); fresh != nil {
		return fresh
	}
	return n
}

func (s *Server) read(c *conn, xid uint32, args *decoder) []byte {
	r, st := s.resolveHandle(args)
	offset := args.uint64()
	count := args.uint32()
	if args.err() != nil {
		return replyHeader(xid, acceptGarbageArgs).bytes()
	}
	if st != nfs3OK {
		return failWithAttr(xid, st, nil)
	}
	n := r.node
	if n.isDir() {
		return failWithAttr(xid, nfs3ErrIsDir, n)
	}
	if count > maxReadBytes {
		count = maxReadBytes
	}
	if offset > uint64(1<<62) {
		return failWithAttr(xid, nfs3ErrInval, n)
	}
	if n.scratch != nil {
		// A client-written file: served from the store, no stream behind it.
		data := n.scratch.data
		var chunk []byte
		if offset < uint64(len(data)) {
			end := min(offset+uint64(count), uint64(len(data)))
			chunk = data[offset:end]
		}
		e := replyHeader(xid, acceptSuccess)
		e.uint32(nfs3OK)
		writePostOpAttr(e, n)
		e.uint32(uint32(len(chunk)))
		e.bool(offset+uint64(len(chunk)) >= uint64(len(data)))
		e.opaque(chunk)
		return e.bytes()
	}
	stream := s.streamForKey(r.h.stream)
	if stream == nil {
		// The token the mount used no longer names a stream.
		return failWithAttr(xid, nfs3ErrStale, nil)
	}
	if s.opts.Open == nil {
		return failWithAttr(xid, nfs3ErrNotSupp, n)
	}
	item := n.item
	clientAddr := c.addr
	open := func(ctx context.Context) (io.ReadSeekCloser, int64, error) {
		rs, size, err := s.opts.Open(ctx, stream, item, clientAddr)
		if err != nil {
			return nil, 0, err
		}
		if size <= 0 {
			size = n.size
		} else if size != n.size {
			s.noteSize(item.ID, size)
		}
		return rs, size, nil
	}
	readStart := time.Now()
	data, eof, err := s.files.readAt(s.ctx, openKey{stream: r.h.stream, node: n.id}, open, int64(offset), int(count))
	if errors.Is(err, errOpening) {
		logger.Debug("NFS read deferred while the stream opens", "client", c.addr, "file", n.name, "offset", offset, "count", count)
		// The stream is still being opened — a cold release takes seconds.
		// JUKEBOX tells the client to ask again shortly, which it does on
		// its own; a reply that merely took that long would run into its
		// RPC timeout instead.
		return failWithAttr(xid, nfs3ErrJukebox, n)
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.Debug("NFS read failed", "client", c.addr, "file", n.path, "offset", offset, "count", count, "err", err)
		}
		return failWithAttr(xid, nfs3ErrIO, n)
	}
	if took := time.Since(readStart); took > time.Second {
		// A slow read is the thing a stalled player is waiting on; said at
		// Debug so it is in the log without trace-level noise.
		logger.Debug("NFS read served slowly", "client", c.addr, "file", n.name, "offset", offset, "count", count, "bytes", len(data), "eof", eof, "ms", took.Milliseconds())
	} else {
		logger.Trace("NFS read served", "client", c.addr, "file", n.name, "offset", offset, "count", count, "bytes", len(data), "eof", eof, "ms", took.Milliseconds())
	}
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	writePostOpAttr(e, n)
	e.uint32(uint32(len(data)))
	e.bool(eof)
	e.opaque(data)
	return e.bytes()
}

// readdir answers READDIR and READDIRPLUS. Entries are the directory's
// children after "." and "..", cookies are 1-based positions in that list,
// and the verifier is the tree generation so a listing paged across a
// content change is told to start over.
func (s *Server) readdir(xid uint32, args *decoder, plus bool) []byte {
	r, st := s.resolveHandle(args)
	cookie := args.uint64()
	verf := args.fixedOpaque(8)
	var dircount, maxcount uint32
	if plus {
		dircount = args.uint32()
	}
	maxcount = args.uint32()
	if args.err() != nil {
		return replyHeader(xid, acceptGarbageArgs).bytes()
	}
	if st != nfs3OK {
		return failWithAttr(xid, st, nil)
	}
	dir := r.node
	if !dir.isDir() {
		return failWithAttr(xid, nfs3ErrNotDir, dir)
	}
	ourVerf := r.tree.cookieVerf()
	if cookie != 0 && !allZero(verf) && string(verf) != string(ourVerf) {
		return failWithAttr(xid, nfs3ErrBadCookie, dir)
	}

	parent := dir.parent
	if parent == nil {
		parent = dir
	}
	entries := make([]*node, 0, len(dir.children)+2)
	names := make([]string, 0, len(dir.children)+2)
	entries = append(entries, dir, parent)
	names = append(names, ".", "..")
	for _, c := range dir.children {
		entries = append(entries, c)
		names = append(names, c.name)
	}
	if cookie > uint64(len(entries)) {
		return failWithAttr(xid, nfs3ErrBadCookie, dir)
	}

	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	writePostOpAttr(e, dir)
	e.fixedOpaque(ourVerf)
	// What the reply costs before any entry: header, status, attrs,
	// verifier, the closing "no more entries" and eof.
	replyBytes := len(e.buf) + 8
	dirBytes := 0
	wrote := 0
	eof := true
	for i := int(cookie); i < len(entries); i++ {
		n := entries[i]
		name := names[i]
		nameBytes := 4 + ((len(name) + 3) &^ 3)
		entryDir := 4 + 8 + nameBytes + 8 // nextentry, fileid, name, cookie
		entryAll := entryDir
		if plus {
			entryAll += 4 + fattr3Bytes + 4 + 4 + ((handleLen + 3) &^ 3)
		}
		if replyBytes+entryAll > int(maxcount) || (plus && dirBytes+entryDir > int(dircount)) {
			if wrote == 0 {
				return failWithAttr(xid, nfs3ErrTooSmall, dir)
			}
			eof = false
			break
		}
		e.bool(true)
		e.uint64(n.id.fileID())
		e.string(name)
		e.uint64(uint64(i + 1))
		if plus {
			writePostOpAttr(e, n)
			e.bool(true)
			e.opaque(r.handleFor(n).encode())
		}
		replyBytes += entryAll
		dirBytes += entryDir
		wrote++
	}
	e.bool(false)
	e.bool(eof)
	return e.bytes()
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
