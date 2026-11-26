package nfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/persistence"
)

// memExport is an in-memory export store with the persistence package's
// semantics, enough for the protocol tests.
type memExport struct {
	mu    sync.Mutex
	next  int64
	nodes map[int64]*persistence.ExportNode
}

func newMemExport() *memExport { return &memExport{nodes: map[int64]*persistence.ExportNode{}} }

func (m *memExport) EnsureRoots() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.childLocked(0, persistence.ExportRootDownloads); !ok {
		m.insertLocked(0, persistence.ExportRootDownloads, persistence.ExportKindDir, "")
	}
	return nil
}

func (m *memExport) insertLocked(parent int64, name, kind, item string) persistence.ExportNode {
	m.next++
	n := &persistence.ExportNode{ID: m.next, ParentID: parent, Name: name, Kind: kind, ItemID: item, CreatedAt: time.Now(), ModifiedAt: time.Now()}
	m.nodes[n.ID] = n
	return *n
}

func (m *memExport) childLocked(parent int64, name string) (persistence.ExportNode, bool) {
	for _, n := range m.nodes {
		if n.ParentID == parent && n.Name == name {
			return *n, true
		}
	}
	return persistence.ExportNode{}, false
}

func (m *memExport) Nodes() ([]persistence.ExportNode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]persistence.ExportNode, 0, len(m.nodes))
	for id := int64(1); id <= m.next; id++ {
		if n, ok := m.nodes[id]; ok {
			out = append(out, *n)
		}
	}
	return out, nil
}

func (m *memExport) Child(parent int64, name string) (persistence.ExportNode, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.childLocked(parent, name)
	return n, ok, nil
}

func (m *memExport) Mkdir(parent int64, name string) (persistence.ExportNode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if parent == 0 && name == persistence.ExportReservedLibrary {
		return persistence.ExportNode{}, persistence.ErrExportExists
	}
	if _, ok := m.childLocked(parent, name); ok {
		return persistence.ExportNode{}, persistence.ErrExportExists
	}
	return m.insertLocked(parent, name, persistence.ExportKindDir, ""), nil
}

func (m *memExport) AddFile(parent int64, name, item string) (persistence.ExportNode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.childLocked(parent, name); ok {
		delete(m.nodes, old.ID)
	}
	return m.insertLocked(parent, name, persistence.ExportKindFile, item), nil
}

func (m *memExport) AddScratch(parent int64, name string) (persistence.ExportNode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.childLocked(parent, name); ok {
		return persistence.ExportNode{}, persistence.ErrExportExists
	}
	return m.insertLocked(parent, name, persistence.ExportKindFile, ""), nil
}

func (m *memExport) WriteScratch(id int64, offset uint64, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return persistence.ErrExportNotFound
	}
	if !n.IsScratch() {
		return persistence.ErrExportNotFile
	}
	if offset+uint64(len(data)) > persistence.MaxScratchBytes {
		return persistence.ErrExportTooLarge
	}
	buf := n.Data
	if data == nil {
		if uint64(len(buf)) > offset {
			buf = buf[:offset]
		} else {
			buf = append(buf, make([]byte, int(offset)-len(buf))...)
		}
	} else {
		if end := offset + uint64(len(data)); uint64(len(buf)) < end {
			buf = append(buf, make([]byte, int(end)-len(buf))...)
		}
		copy(buf[offset:], data)
	}
	n.Data = buf
	n.ModifiedAt = time.Now()
	return nil
}

func (m *memExport) Rename(id, newParent int64, newName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return persistence.ErrExportNotFound
	}
	if n.ParentID == 0 && n.Name == persistence.ExportRootDownloads {
		return persistence.ErrExportRoot
	}
	if target, ok := m.childLocked(newParent, newName); ok && target.ID != id {
		for _, c := range m.nodes {
			if c.ParentID == target.ID {
				return persistence.ErrExportNotEmpty
			}
		}
		delete(m.nodes, target.ID)
	}
	n.ParentID, n.Name, n.ModifiedAt = newParent, newName, time.Now()
	return nil
}

func (m *memExport) RemoveTree(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return persistence.ErrExportNotFound
	}
	if n.ParentID == 0 && n.Name == persistence.ExportRootDownloads {
		return persistence.ErrExportRoot
	}
	ids := []int64{id}
	for i := 0; i < len(ids); i++ {
		for _, c := range m.nodes {
			if c.ParentID == ids[i] {
				ids = append(ids, c.ID)
			}
		}
	}
	for _, d := range ids {
		delete(m.nodes, d)
	}
	return nil
}

func (m *memExport) Remove(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return persistence.ErrExportNotFound
	}
	if n.ParentID == 0 && n.Name == persistence.ExportRootDownloads {
		return persistence.ErrExportRoot
	}
	for _, c := range m.nodes {
		if c.ParentID == id {
			return persistence.ErrExportNotEmpty
		}
	}
	delete(m.nodes, id)
	return nil
}

func TestWritableTreeImportFlow(t *testing.T) {
	content := bytes.Repeat([]byte("w"), 2048)
	var closes atomic.Int32
	export := newMemExport()
	item := fixtureItems()[0]
	stream := &auth.Stream{Username: "arr", Token: strings.Repeat("a", 64)}
	s := New(Options{
		Host:    "127.0.0.1",
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Library: &fakeLibrary{items: []*persistence.LibraryItem{item}},
		Export:  export,
		Open: func(context.Context, *auth.Stream, *persistence.LibraryItem, string) (io.ReadSeekCloser, int64, error) {
			return &memFile{Reader: bytes.NewReader(content), closed: &closes}, int64(len(content)), nil
		},
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })

	// The download client finishes a job.
	folder, ok := s.PublishDownload(context.Background(), "The.Shawshank.Redemption.1994.2160p.BluRay-GRP.nzb", item)
	if !ok || folder != "/downloads/The.Shawshank.Redemption.1994.2160p.BluRay-GRP" {
		t.Fatalf("publish: %q %v", folder, ok)
	}
	// Publishing twice is harmless.
	if _, ok := s.PublishDownload(context.Background(), "The.Shawshank.Redemption.1994.2160p.BluRay-GRP", item); !ok {
		t.Fatal("republish failed")
	}

	c := dialTestServer(t, s)
	args := &encoder{}
	args.string("/" + stream.Token)
	d := c.call(progMount, mountVersion, mountProcMnt, args.bytes())
	d.uint32()
	rootFH := d.opaque(64)

	root := s.currentTree().root
	if got := strings.Join(names(root), "|"); got != "downloads|library" {
		t.Fatalf("root = %s", got)
	}
	// library/ is generated and read-only: no mkdir in it, no mkdir of it.
	libFH := lookup(t, c, rootFH, "library", nf3Dir)
	ro := &encoder{}
	ro.opaque(libFH)
	ro.string("x")
	ro.uint32(0)
	for i := 0; i < 6; i++ {
		ro.uint32(0)
	}
	if d := c.call(progNFS, nfsVersion, nfsProcMkdir, ro.bytes()); d.uint32() != nfs3ErrROFS {
		t.Fatal("MKDIR inside library/ must be refused")
	}
	ro = &encoder{}
	ro.opaque(rootFH)
	ro.string("library")
	ro.uint32(0)
	for i := 0; i < 6; i++ {
		ro.uint32(0)
	}
	if d := c.call(progNFS, nfsVersion, nfsProcMkdir, ro.bytes()); d.uint32() != nfs3ErrExist {
		t.Fatal("MKDIR of library at the root must be EEXIST")
	}
	ro = &encoder{}
	ro.opaque(rootFH)
	ro.string("library")
	if d := c.call(progNFS, nfsVersion, nfsProcRmdir, ro.bytes()); d.uint32() != nfs3ErrROFS {
		t.Fatal("RMDIR of library must be refused")
	}
	downloadsFH := lookup(t, c, rootFH, "downloads", nf3Dir)
	jobFH := lookup(t, c, downloadsFH, "The.Shawshank.Redemption.1994.2160p.BluRay-GRP", nf3Dir)
	fileFH := lookup(t, c, jobFH, "shawshank.mkv", nf3Reg)

	// The *arr makes its own root folder at the top of the export.
	mk := &encoder{}
	mk.opaque(rootFH)
	mk.string("media")
	mk.uint32(0)
	for i := 0; i < 6; i++ {
		mk.uint32(0)
	}
	d = c.call(progNFS, nfsVersion, nfsProcMkdir, mk.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("MKDIR at root status %d", st)
	}
	mediaFH := lookup(t, c, rootFH, "media", nf3Dir)

	// ACCESS on any directory grants modify/delete.
	access := func(fh []byte) uint32 {
		a := &encoder{}
		a.opaque(fh)
		a.uint32(0x3f)
		d := c.call(progNFS, nfsVersion, nfsProcAccess, a.bytes())
		d.uint32()
		readPostOpAttr(d)
		return d.uint32()
	}
	if got := access(mediaFH); got&access3Modify == 0 || got&access3Delete == 0 {
		t.Fatalf("media ACCESS = %#x", got)
	}
	if got := access(rootFH); got&access3Modify == 0 {
		t.Fatalf("root ACCESS = %#x, must be writable", got)
	}

	// FSSTAT reports free space, or the *arr refuses to import.
	d = c.call(progNFS, nfsVersion, nfsProcFsstat, fh(rootFH))
	d.uint32()
	readPostOpAttr(d)
	d.uint64()
	if free := d.uint64(); free == 0 {
		t.Fatal("FSSTAT must report free space")
	}

	// The *arr imports: MKDIR media/Shawshank (1994), RENAME the file in,
	// SETATTR (chmod), RMDIR the job folder.
	a := &encoder{}
	a.opaque(mediaFH)
	a.string("The Shawshank Redemption (1994)")
	a.uint32(1) // sattr3: mode set
	a.uint32(0o755)
	for i := 0; i < 5; i++ {
		a.uint32(0) // uid, gid, size, atime, mtime: not set
	}
	d = c.call(progNFS, nfsVersion, nfsProcMkdir, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("MKDIR status %d", st)
	}
	if !d.bool() {
		t.Fatal("MKDIR must return the new handle")
	}
	movieDirFH := d.opaque(64)
	if present, ftype, _ := readPostOpAttr(d); !present || ftype != nf3Dir {
		t.Fatal("MKDIR attrs")
	}
	// Same name again is EEXIST.
	d = c.call(progNFS, nfsVersion, nfsProcMkdir, a.bytes())
	if st := d.uint32(); st != nfs3ErrExist {
		t.Fatalf("duplicate MKDIR status %d", st)
	}

	a = &encoder{}
	a.opaque(jobFH)
	a.string("shawshank.mkv")
	a.opaque(movieDirFH)
	a.string("The Shawshank Redemption (1994) Bluray-2160p.mkv")
	d = c.call(progNFS, nfsVersion, nfsProcRename, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("RENAME status %d", st)
	}
	movedFH := lookup(t, c, movieDirFH, "The Shawshank Redemption (1994) Bluray-2160p.mkv", nf3Reg)
	if !bytes.Equal(movedFH, fileFH) {
		t.Fatal("a rename must keep the file handle")
	}
	// The old name is gone.
	a = &encoder{}
	a.opaque(jobFH)
	a.string("shawshank.mkv")
	d = c.call(progNFS, nfsVersion, nfsProcLookup, a.bytes())
	if st := d.uint32(); st != nfs3ErrNoEnt {
		t.Fatalf("old name status %d", st)
	}

	a = &encoder{}
	a.opaque(movedFH)
	a.uint32(1)
	a.uint32(0o644)
	for i := 0; i < 5; i++ {
		a.uint32(0)
	}
	a.uint32(0) // guard: no check
	d = c.call(progNFS, nfsVersion, nfsProcSetattr, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("SETATTR status %d", st)
	}

	a = &encoder{}
	a.opaque(downloadsFH)
	a.string("The.Shawshank.Redemption.1994.2160p.BluRay-GRP")
	d = c.call(progNFS, nfsVersion, nfsProcRmdir, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("RMDIR status %d", st)
	}

	// The moved file still reads its library row.
	a = &encoder{}
	a.opaque(movedFH)
	a.uint64(0)
	a.uint32(16)
	d = c.call(progNFS, nfsVersion, nfsProcRead, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("READ status %d", st)
	}
	readPostOpAttr(d)
	if count := d.uint32(); count != 16 {
		t.Fatalf("READ count %d", count)
	}

	// REMOVE the file; the downloads folder itself refuses.
	a = &encoder{}
	a.opaque(movieDirFH)
	a.string("The Shawshank Redemption (1994) Bluray-2160p.mkv")
	d = c.call(progNFS, nfsVersion, nfsProcRemove, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("REMOVE status %d", st)
	}
	if closes.Load() != 1 {
		t.Fatalf("removing an open alias must close its stream, closes=%d", closes.Load())
	}
	a = &encoder{}
	a.opaque(rootFH)
	a.string("downloads")
	d = c.call(progNFS, nfsVersion, nfsProcRmdir, a.bytes())
	if st := d.uint32(); st != nfs3ErrAcces {
		t.Fatalf("RMDIR downloads status %d", st)
	}
	a = &encoder{}
	a.opaque(rootFH)
	a.string("downloads")
	a.opaque(rootFH)
	a.string("elsewhere")
	d = c.call(progNFS, nfsVersion, nfsProcRename, a.bytes())
	if st := d.uint32(); st != nfs3ErrAcces {
		t.Fatalf("RENAME downloads status %d", st)
	}
	// A client may create its own small files (sidecars, write tests).
	a = &encoder{}
	a.opaque(movieDirFH)
	a.string("movie.nfo")
	a.uint32(0)
	a.uint32(0)
	for i := 0; i < 6; i++ {
		a.uint32(0)
	}
	d = c.call(progNFS, nfsVersion, nfsProcCreate, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("CREATE status %d", st)
	}
}

func TestExportStatusMapping(t *testing.T) {
	cases := map[error]uint32{
		nil:                            nfs3OK,
		persistence.ErrExportExists:    nfs3ErrExist,
		persistence.ErrExportNotEmpty:  nfs3ErrNotEmpty,
		persistence.ErrExportNotFound:  nfs3ErrNoEnt,
		persistence.ErrExportRoot:      nfs3ErrAcces,
		persistence.ErrExportBadName:   nfs3ErrInval,
		errors.New("database on fire"): nfs3ErrIO,
	}
	for err, want := range cases {
		if got := exportStatus(err); got != want {
			t.Errorf("exportStatus(%v) = %d, want %d", err, got, want)
		}
	}
}

func TestScratchFilesAreWritableAndReleasesAreNot(t *testing.T) {
	content := bytes.Repeat([]byte("r"), 1024)
	var closes atomic.Int32
	item := fixtureItems()[0]
	stream := &auth.Stream{Username: "arr", Token: strings.Repeat("a", 64)}
	s := New(Options{
		Host:    "127.0.0.1",
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Library: &fakeLibrary{items: []*persistence.LibraryItem{item}},
		Export:  newMemExport(),
		Open: func(context.Context, *auth.Stream, *persistence.LibraryItem, string) (io.ReadSeekCloser, int64, error) {
			return &memFile{Reader: bytes.NewReader(content), closed: &closes}, int64(len(content)), nil
		},
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })
	s.PublishDownload(context.Background(), "Job", item)
	c := dialTestServer(t, s)
	rootFH := mountRoot(t, c, stream.Token)

	// The *arr's writability check: CREATE, WRITE, READ back, REMOVE.
	a := &encoder{}
	a.opaque(rootFH)
	a.string("radarr_write_test.txt")
	a.uint32(createUnchecked)
	a.uint32(1)
	a.uint32(0o644)
	for i := 0; i < 5; i++ {
		a.uint32(0)
	}
	d := c.call(progNFS, nfsVersion, nfsProcCreate, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("CREATE status %d", st)
	}
	if !d.bool() {
		t.Fatal("CREATE must return a handle")
	}
	fileFH := d.opaque(64)
	if present, ftype, size := readPostOpAttr(d); !present || ftype != nf3Reg || size != 0 {
		t.Fatalf("CREATE attrs present=%v type=%d size=%d", present, ftype, size)
	}

	a = &encoder{}
	a.opaque(fileFH)
	a.uint64(0)
	a.uint32(5)
	a.uint32(writeFileSync)
	a.opaque([]byte("hello"))
	d = c.call(progNFS, nfsVersion, nfsProcWrite, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("WRITE status %d", st)
	}
	d.bool() // wcc before
	if present, _, size := readPostOpAttr(d); !present || size != 5 {
		t.Fatalf("WRITE wcc size %d", size)
	}
	if count := d.uint32(); count != 5 {
		t.Fatalf("WRITE count %d", count)
	}
	// A second write past the end zero-fills the gap.
	a = &encoder{}
	a.opaque(fileFH)
	a.uint64(8)
	a.uint32(2)
	a.uint32(writeFileSync)
	a.opaque([]byte("!!"))
	d = c.call(progNFS, nfsVersion, nfsProcWrite, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("WRITE 2 status %d", st)
	}
	a = &encoder{}
	a.opaque(fileFH)
	a.uint64(0)
	a.uint32(64)
	d = c.call(progNFS, nfsVersion, nfsProcRead, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("READ status %d", st)
	}
	readPostOpAttr(d)
	count := d.uint32()
	eof := d.bool()
	data := d.opaque(64)
	if count != 10 || !eof || string(data) != "hello\x00\x00\x00!!" {
		t.Fatalf("READ back count=%d eof=%v data=%q", count, eof, data)
	}
	// SETATTR size truncates.
	a = &encoder{}
	a.opaque(fileFH)
	a.uint32(0)
	a.uint32(0)
	a.uint32(0)
	a.uint32(1)
	a.uint64(3)
	a.uint32(0)
	a.uint32(0)
	a.uint32(0) // guard
	d = c.call(progNFS, nfsVersion, nfsProcSetattr, a.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("SETATTR status %d", st)
	}
	d.bool()
	if present, _, size := readPostOpAttr(d); !present || size != 3 {
		t.Fatalf("truncated size %d", size)
	}
	// COMMIT is fine.
	a = &encoder{}
	a.opaque(fileFH)
	a.uint64(0)
	a.uint32(0)
	if d := c.call(progNFS, nfsVersion, nfsProcCommit, a.bytes()); d.uint32() != nfs3OK {
		t.Fatal("COMMIT failed")
	}
	a = &encoder{}
	a.opaque(rootFH)
	a.string("radarr_write_test.txt")
	if d := c.call(progNFS, nfsVersion, nfsProcRemove, a.bytes()); d.uint32() != nfs3OK {
		t.Fatal("REMOVE failed")
	}

	// A release stays read-only to WRITE and to truncation.
	downloads := lookup(t, c, rootFH, "downloads", nf3Dir)
	job := lookup(t, c, downloads, "Job", nf3Dir)
	rel := lookup(t, c, job, item.MediaFileName, nf3Reg)
	a = &encoder{}
	a.opaque(rel)
	a.uint64(0)
	a.uint32(1)
	a.uint32(writeFileSync)
	a.opaque([]byte("x"))
	if d := c.call(progNFS, nfsVersion, nfsProcWrite, a.bytes()); d.uint32() != nfs3ErrROFS {
		t.Fatal("WRITE to a release must be EROFS")
	}
	a = &encoder{}
	a.opaque(rel)
	a.uint32(0)
	a.uint32(0)
	a.uint32(0)
	a.uint32(1)
	a.uint64(0)
	a.uint32(0)
	a.uint32(0)
	a.uint32(0)
	if d := c.call(progNFS, nfsVersion, nfsProcSetattr, a.bytes()); d.uint32() != nfs3ErrROFS {
		t.Fatal("truncating a release must be EROFS")
	}
	// Too big is EFBIG.
	a = &encoder{}
	a.opaque(rootFH)
	a.string("big.bin")
	a.uint32(createUnchecked)
	for i := 0; i < 6; i++ {
		a.uint32(0)
	}
	d = c.call(progNFS, nfsVersion, nfsProcCreate, a.bytes())
	d.uint32()
	d.bool()
	bigFH := d.opaque(64)
	a = &encoder{}
	a.opaque(bigFH)
	a.uint64(maxScratchBytes)
	a.uint32(1)
	a.uint32(writeFileSync)
	a.opaque([]byte("x"))
	if d := c.call(progNFS, nfsVersion, nfsProcWrite, a.bytes()); d.uint32() != nfs3ErrFBig {
		t.Fatal("write past the cap must be EFBIG")
	}
}

func TestRmdirClearsGhostAliases(t *testing.T) {
	export := newMemExport()
	item := fixtureItems()[0]
	stream := &auth.Stream{Username: "arr", Token: strings.Repeat("a", 64)}
	lib := &fakeLibrary{items: []*persistence.LibraryItem{item}}
	s := New(Options{
		Host:    "127.0.0.1",
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Library: lib,
		Export:  export,
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })
	if _, ok := s.PublishDownload(context.Background(), "Job", item); !ok {
		t.Fatal("publish")
	}
	// The row goes away (sweep marked it bad, say): the alias is now a ghost.
	lib.items = nil
	s.markDirty()
	c := dialTestServer(t, s)
	rootFH := mountRoot(t, c, stream.Token)
	downloadsFH := lookup(t, c, rootFH, "downloads", nf3Dir)
	jobFH := lookup(t, c, downloadsFH, "Job", nf3Dir)
	a := &encoder{}
	a.opaque(jobFH)
	a.uint64(0)
	a.fixedOpaque(make([]byte, 8))
	a.uint32(4096)
	a.uint32(32768)
	d := c.call(progNFS, nfsVersion, nfsProcReaddirplus, a.bytes())
	d.uint32()
	readPostOpAttr(d)
	d.fixedOpaque(8)
	entries := 0
	for d.bool() {
		d.uint64()
		d.string(maxFileName)
		d.uint64()
		readPostOpAttr(d)
		if d.bool() {
			d.opaque(64)
		}
		entries++
	}
	if entries != 2 {
		t.Fatalf("ghost alias must be hidden: %d entries", entries)
	}
	a = &encoder{}
	a.opaque(downloadsFH)
	a.string("Job")
	if d := c.call(progNFS, nfsVersion, nfsProcRmdir, a.bytes()); d.uint32() != nfs3OK {
		t.Fatal("RMDIR of a folder holding only ghost aliases must succeed")
	}
	if nodes, _ := export.Nodes(); len(nodes) != 1 {
		t.Fatalf("ghost aliases must be gone from the store, %d nodes left", len(nodes))
	}
}
