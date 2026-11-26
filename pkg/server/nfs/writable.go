package nfs

import (
	"context"
	"errors"
	"path"
	"strings"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

// The export is a tree of names the client chose, kept in the export store;
// a file in it is an alias to a library row. Only "downloads" at the top is
// fixed, because the download client API publishes into it. Radarr and Sonarr import a finished
// download by renaming it into their library folder, and that is all the
// write support they need: MKDIR, RENAME, REMOVE and RMDIR, plus a SETATTR
// that says yes to permission and time changes it does not keep. Nothing
// here moves media, because none is stored. CREATE and WRITE stay refused:
// a file here is a release, and releases arrive through the download client
// API or by being played.

// Export is the slice of the export store the server needs.
type Export interface {
	EnsureRoots() error
	Nodes() ([]persistence.ExportNode, error)
	Child(parent int64, name string) (persistence.ExportNode, bool, error)
	Mkdir(parent int64, name string) (persistence.ExportNode, error)
	AddFile(parent int64, name, itemID string) (persistence.ExportNode, error)
	AddScratch(parent int64, name string) (persistence.ExportNode, error)
	WriteScratch(id int64, offset uint64, data []byte) error
	Rename(id, newParent int64, newName string) error
	Remove(id int64) error
	RemoveTree(id int64) error
}

// vfsPath names an export-store node in the handle space: stable across
// renames, because a client keeps using the handle it has after a rename.
func vfsPath(id int64) string {
	return "vfs:" + itoa(id)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// exportStatus maps a store error onto the nfsstat3 the client expects.
func exportStatus(err error) uint32 {
	switch {
	case err == nil:
		return nfs3OK
	case errors.Is(err, persistence.ErrExportExists):
		return nfs3ErrExist
	case errors.Is(err, persistence.ErrExportNotEmpty):
		return nfs3ErrNotEmpty
	case errors.Is(err, persistence.ErrExportNotFound):
		return nfs3ErrNoEnt
	case errors.Is(err, persistence.ErrExportRoot):
		return nfs3ErrAcces
	case errors.Is(err, persistence.ErrExportBadName):
		return nfs3ErrInval
	case errors.Is(err, persistence.ErrExportTooLarge):
		return nfs3ErrFBig
	case errors.Is(err, persistence.ErrExportNotFile):
		return nfs3ErrROFS
	}
	return nfs3ErrIO
}

// writableDir reports whether n is a directory the client may change: the
// root and every store-backed directory. The generated library folder is
// not, and the downloads folder's name and place are refused by the store.
func writableDir(n *node) bool {
	return n != nil && n.isDir() && !n.generated()
}

// --- procedures ---------------------------------------------------------

// mkdir: MKDIR3args { dir, name, attributes } → MKDIR3res { obj fh, obj
// attrs, dir wcc }.
func (s *Server) mkdir(xid uint32, args *decoder) []byte {
	r, st := s.resolveHandle(args)
	name := args.string(maxFileName)
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
	created, err := s.opts.Export.Mkdir(dir.vfsID, name)
	if err != nil {
		return failWcc(xid, exportStatus(err), dir)
	}
	t := s.refreshTree()
	obj := t.lookup(nodeIDFor(kindDir, vfsPath(created.ID)))
	dirNow := t.lookup(dir.id)
	logger.Debug("NFS export mkdir", "path", path.Join(dir.path, name))
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	if obj != nil {
		e.bool(true)
		e.opaque(r.handleFor(obj).encode())
	} else {
		e.bool(false)
	}
	writePostOpAttr(e, obj)
	writeWccData(e, dirNow)
	return e.bytes()
}

// remove and rmdir: diropargs3 { dir, name } → wcc_data dir.
func (s *Server) remove(xid uint32, args *decoder, wantDir bool) []byte {
	r, st := s.resolveHandle(args)
	name := args.string(maxFileName)
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
	target := dir.child(name)
	if target == nil {
		return failWcc(xid, nfs3ErrNoEnt, dir)
	}
	switch {
	case wantDir && !target.isDir():
		return failWcc(xid, nfs3ErrNotDir, dir)
	case !wantDir && target.isDir():
		return failWcc(xid, nfs3ErrIsDir, dir)
	case target.generated():
		return failWcc(xid, nfs3ErrROFS, dir)
	}
	var err error
	if wantDir && len(target.children) == 0 {
		// Empty as the client sees it. Anything still stored beneath it is
		// an alias of a row that is gone, hidden from the listing; an RMDIR
		// must not fail on those ghosts.
		err = s.opts.Export.RemoveTree(target.vfsID)
	} else {
		err = s.opts.Export.Remove(target.vfsID)
	}
	if err != nil {
		return failWcc(xid, exportStatus(err), dir)
	}
	// The file is gone; any stream open on it ends.
	s.files.closeKey(openKey{stream: r.h.stream, node: target.id})
	t := s.refreshTree()
	logger.Debug("NFS export remove", "path", target.path, "dir", target.isDir())
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	writeWccData(e, t.lookup(dir.id))
	return e.bytes()
}

// rename: RENAME3args { from diropargs, to diropargs } → fromdir wcc, todir wcc.
func (s *Server) rename(xid uint32, args *decoder) []byte {
	from, stFrom := s.resolveHandle(args)
	fromName := args.string(maxFileName)
	to, stTo := s.resolveHandle(args)
	toName := args.string(maxFileName)
	if args.err() != nil {
		return replyHeader(xid, acceptGarbageArgs).bytes()
	}
	fail := func(st uint32) []byte {
		e := replyHeader(xid, acceptSuccess)
		e.uint32(st)
		writeWccData(e, from.node)
		writeWccData(e, to.node)
		return e.bytes()
	}
	if stFrom != nfs3OK {
		return fail(stFrom)
	}
	if stTo != nfs3OK {
		return fail(stTo)
	}
	if !writableDir(from.node) || !writableDir(to.node) {
		return fail(nfs3ErrROFS)
	}
	if from.h.stream != to.h.stream {
		return fail(nfs3ErrXDev)
	}
	if s.opts.Export == nil {
		return fail(nfs3ErrNotSupp)
	}
	src := from.node.child(fromName)
	if src == nil {
		return fail(nfs3ErrNoEnt)
	}
	if src.generated() {
		return fail(nfs3ErrROFS)
	}
	if err := s.opts.Export.Rename(src.vfsID, to.node.vfsID, toName); err != nil {
		return fail(exportStatus(err))
	}
	t := s.refreshTree()
	logger.Debug("NFS export rename", "from", src.path, "to", path.Join(to.node.path, toName))
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	writeWccData(e, t.lookup(from.node.id))
	writeWccData(e, t.lookup(to.node.id))
	return e.bytes()
}

// setattr on a writable node is accepted: permissions and times are not
// kept (the *arr's chmod after import must not fail), and a size change on a
// scratch file truncates or extends it. A size change on a release refuses.
func (s *Server) setattr(xid uint32, args *decoder) []byte {
	r, st := s.resolveHandle(args)
	attrs := readSattr3(args)
	if st != nfs3OK {
		return failWcc(xid, st, nil)
	}
	n := r.node
	if n.generated() {
		return failWcc(xid, nfs3ErrROFS, n)
	}
	if attrs.setSize && !n.isDir() {
		if n.scratch == nil {
			return failWcc(xid, nfs3ErrROFS, n)
		}
		if err := s.truncateScratch(n, attrs.size); err != nil {
			return failWcc(xid, exportStatus(err), n)
		}
		n = s.refreshTree().lookup(n.id)
	}
	e := replyHeader(xid, acceptSuccess)
	e.uint32(nfs3OK)
	writeWccData(e, n)
	return e.bytes()
}

// failWcc is a failure reply whose result is a wcc_data.
func failWcc(xid, status uint32, n *node) []byte {
	e := replyHeader(xid, acceptSuccess)
	e.uint32(status)
	writeWccData(e, n)
	return e.bytes()
}

// refreshTree rebuilds the tree now, after a write, so the reply describes
// what the client just did.
func (s *Server) refreshTree() *tree {
	s.markDirty()
	return s.currentTree()
}

// --- publishing ---------------------------------------------------------

// PublishDownload places a finished download-client job in the tree as
// downloads/<job>/<media file>, the way SABnzbd leaves a completed
// job in its complete folder, and returns that folder's path on the export.
// The *arr is handed the folder, finds the file in it, and renames it into
// its own library folder on the same export. ok is false when there is no
// export store to publish into.
func (s *Server) PublishDownload(_ context.Context, jobName string, item *persistence.LibraryItem) (string, bool) {
	if s == nil || s.opts.Export == nil || item == nil {
		return "", false
	}
	if err := s.opts.Export.EnsureRoots(); err != nil {
		logger.Warn("NFS export could not create its root folders", "err", err)
		return "", false
	}
	root, ok, err := s.opts.Export.Child(0, persistence.ExportRootDownloads)
	if err != nil || !ok {
		return "", false
	}
	folder := sanitizeName(strings.TrimSuffix(strings.TrimSpace(jobName), ".nzb"), maxNameBytes)
	dir, exists, err := s.opts.Export.Child(root.ID, folder)
	if err != nil {
		return "", false
	}
	if !exists {
		if dir, err = s.opts.Export.Mkdir(root.ID, folder); err != nil {
			logger.Warn("NFS export could not create a download folder", "folder", folder, "err", err)
			return "", false
		}
	}
	fileName := strings.TrimSpace(item.MediaFileName)
	if fileName == "" {
		fileName = sanitizeName(item.ReleaseTitle, maxNameBytes-len(defaultMediaExt)) + defaultMediaExt
	}
	if _, err := s.opts.Export.AddFile(dir.ID, sanitizeName(fileName, maxNameBytes), item.ID); err != nil {
		logger.Warn("NFS export could not publish a download", "folder", folder, "file", fileName, "err", err)
		return "", false
	}
	s.refreshTree()
	logger.Info("NFS export published download", "folder", folder, "file", fileName)
	return "/" + persistence.ExportRootDownloads + "/" + folder, true
}
