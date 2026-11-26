package nfs

import (
	"errors"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

// Scratch files are the small files a client writes itself: the write-test
// file Radarr and Sonarr drop in a folder before they accept it as a root
// folder, and the .nfo and subtitle sidecars they leave next to an import.
// They are stored in the export store, capped in size, and served from it.
// A release stays what it was — an alias to a library row that no WRITE can
// touch — so the only files a client can write are the ones it created.

const (
	// maxScratchBytes caps one scratch file. Subtitles run to a few hundred
	// kilobytes; nothing a client should write here is media.
	maxScratchBytes = 8 << 20

	createUnchecked = 0
	createGuarded   = 1
	createExclusive = 2

	// WRITE stable_how / committed
	writeFileSync = 2

	// sattr3 time_how
	timeDontChange = 0
)

// sattr3 is the attributes a SETATTR or CREATE asks to set. Only the size
// matters here (a truncate); the rest is parsed to get past it and ignored.
type sattr3 struct {
	setSize bool
	size    uint64
}

func readSattr3(d *decoder) sattr3 {
	var a sattr3
	if d.bool() { // mode
		d.uint32()
	}
	if d.bool() { // uid
		d.uint32()
	}
	if d.bool() { // gid
		d.uint32()
	}
	if d.bool() { // size
		a.setSize = true
		a.size = d.uint64()
	}
	for i := 0; i < 2; i++ { // atime, mtime
		if how := d.uint32(); how != timeDontChange && how != 1 {
			d.uint32()
			d.uint32()
		}
	}
	return a
}

// writeVerf is the write verifier a client uses to detect a server restart
// between a WRITE and its COMMIT. Scratch writes are durable at once, so a
// constant is honest.
var writeVerf = [8]byte{'S', 'N', 'Z', 'B', 's', 'c', 'r', '1'}

// create: CREATE3args { where diropargs3, how createhow3 } → CREATE3res {
// obj post_op_fh3, obj_attributes, dir_wcc }.
func (s *Server) create(xid uint32, args *decoder) []byte {
	r, st := s.resolveHandle(args)
	name := args.string(maxFileName)
	mode := args.uint32()
	var attrs sattr3
	switch mode {
	case createUnchecked, createGuarded:
		attrs = readSattr3(args)
	case createExclusive:
		args.fixedOpaque(8)
	}
	if args.err() != nil {
		return replyHeader(xid, acceptGarbageArgs).bytes()
	}
	if st != nfs3OK {
		return failWcc(xid, st, nil)
	}
	dir := r.node
	if !writableDir(dir) {
		return failWcc(xid, nfs3ErrROFS, dir)
	}
	if s.opts.Export == nil {
		return failWcc(xid, nfs3ErrNotSupp, dir)
	}
	if existing := dir.child(name); existing != nil {
		if mode != createUnchecked || existing.isDir() || existing.scratch == nil {
			return failWcc(xid, nfs3ErrExist, dir)
		}
		// UNCHECKED on an existing scratch file: truncate if asked, else keep.
		if attrs.setSize {
			if err := s.opts.Export.WriteScratch(existing.vfsID, attrs.size, nil); err != nil {
				return failWcc(xid, exportStatus(err), dir)
			}
		}
		t := s.refreshTree()
		return createReply(xid, r, t.lookup(existing.id), t.lookup(dir.id))
	}
	created, err := s.opts.Export.AddScratch(dir.vfsID, name)
	if err != nil {
		return failWcc(xid, exportStatus(err), dir)
	}
	t := s.refreshTree()
	logger.Debug("NFS export create", "path", dir.path+"/"+name)
	return createReply(xid, r, t.lookup(nodeIDFor(kindFile, vfsPath(created.ID))), t.lookup(dir.id))
}

func createReply(xid uint32, r resolved, obj, dir *node) []byte {
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	if obj != nil {
		e.bool(true)
		e.opaque(r.handleFor(obj).encode())
	} else {
		e.bool(false)
	}
	writePostOpAttr(e, obj)
	writeWccData(e, dir)
	return e.bytes()
}

// write: WRITE3args { file, offset, count, stable, data } → WRITE3res {
// file_wcc, count, committed, verf }. Only scratch files take writes.
func (s *Server) write(xid uint32, args *decoder) []byte {
	r, st := s.resolveHandle(args)
	offset := args.uint64()
	count := args.uint32()
	args.uint32() // stable_how: everything here is FILE_SYNC
	data := args.opaque(maxReadBytes)
	if args.err() != nil {
		return replyHeader(xid, acceptGarbageArgs).bytes()
	}
	if st != nfs3OK {
		return failWcc(xid, st, nil)
	}
	n := r.node
	if n.isDir() {
		return failWcc(xid, nfs3ErrIsDir, n)
	}
	if n.scratch == nil || s.opts.Export == nil {
		return failWcc(xid, nfs3ErrROFS, n)
	}
	if int(count) < len(data) {
		data = data[:count]
	}
	if offset+uint64(len(data)) > maxScratchBytes {
		return failWcc(xid, nfs3ErrFBig, n)
	}
	if err := s.opts.Export.WriteScratch(n.vfsID, offset, data); err != nil {
		return failWcc(xid, exportStatus(err), n)
	}
	t := s.refreshTree()
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	writeWccData(e, t.lookup(n.id))
	e.uint32(uint32(len(data)))
	e.uint32(writeFileSync)
	e.fixedOpaque(writeVerf[:])
	return e.bytes()
}

// commit: COMMIT3args { file, offset, count } → COMMIT3res { file_wcc, verf }.
// Scratch writes are already durable; a release has nothing to commit.
func (s *Server) commit(xid uint32, args *decoder) []byte {
	r, st := s.resolveHandle(args)
	if st != nfs3OK {
		return failWcc(xid, st, nil)
	}
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	writeWccData(e, r.node)
	e.fixedOpaque(writeVerf[:])
	return e.bytes()
}

// truncateScratch applies a SETATTR size change to a scratch file.
func (s *Server) truncateScratch(n *node, size uint64) error {
	if n.scratch == nil || s.opts.Export == nil {
		return errors.New("not a scratch file")
	}
	if size > maxScratchBytes {
		return persistence.ErrExportTooLarge
	}
	return s.opts.Export.WriteScratch(n.vfsID, size, nil)
}
