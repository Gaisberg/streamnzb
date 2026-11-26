package nfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/persistence"
)

// --- XDR and RPC framing -----------------------------------------------

func TestXDRRoundTrip(t *testing.T) {
	e := &encoder{}
	e.uint32(7)
	e.uint64(1 << 40)
	e.bool(true)
	e.opaque([]byte("abcde")) // 5 bytes → 3 bytes of padding
	e.string("hi")
	e.fixedOpaque([]byte{1, 2, 3, 4, 5, 6, 7, 8})

	d := newDecoder(e.bytes())
	if got := d.uint32(); got != 7 {
		t.Fatalf("uint32 = %d", got)
	}
	if got := d.uint64(); got != 1<<40 {
		t.Fatalf("uint64 = %d", got)
	}
	if !d.bool() {
		t.Fatal("bool = false")
	}
	if got := d.opaque(64); string(got) != "abcde" {
		t.Fatalf("opaque = %q", got)
	}
	if got := d.string(64); got != "hi" {
		t.Fatalf("string = %q", got)
	}
	if got := d.fixedOpaque(8); !bytes.Equal(got, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("fixedOpaque = %v", got)
	}
	if d.err() != nil || len(d.remaining()) != 0 {
		t.Fatalf("decoder should be exhausted cleanly: err=%v remaining=%d", d.err(), len(d.remaining()))
	}
	d.uint32()
	if d.err() == nil {
		t.Fatal("short read did not set the error")
	}
}

func TestDecoderRefusesOversizedOpaque(t *testing.T) {
	e := &encoder{}
	e.uint32(1 << 30)
	d := newDecoder(e.bytes())
	if got := d.opaque(1024); got != nil || d.err() == nil {
		t.Fatal("oversized opaque was not refused")
	}
}

func TestRecordMarkingReassemblesFragments(t *testing.T) {
	var wire []byte
	frag := func(b []byte, last bool) {
		mark := uint32(len(b))
		if last {
			mark |= 0x80000000
		}
		wire = binary.BigEndian.AppendUint32(wire, mark)
		wire = append(wire, b...)
	}
	frag([]byte("hello "), false)
	frag([]byte("world"), true)

	got, err := readRecord(bytes.NewReader(wire), maxRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("record = %q", got)
	}
	framed := frameRecord([]byte("x"))
	if binary.BigEndian.Uint32(framed)&0x80000000 == 0 || len(framed) != 5 {
		t.Fatalf("frameRecord = %v", framed)
	}
}

func TestRecordMarkingRefusesHugeRecord(t *testing.T) {
	wire := binary.BigEndian.AppendUint32(nil, 0x80000000|uint32(maxRecordBytes+1))
	if _, err := readRecord(bytes.NewReader(wire), maxRecordBytes); err == nil {
		t.Fatal("oversized record was accepted")
	}
}

// --- handles -----------------------------------------------------------

func TestHandleRoundTrip(t *testing.T) {
	h := handle{kind: kindFile, stream: streamKeyFor("tok"), node: nodeIDFor(kindFile, vfsPath(42))}
	raw := h.encode()
	if len(raw) != handleLen {
		t.Fatalf("handle is %d bytes, want %d", len(raw), handleLen)
	}
	back, err := decodeHandle(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back != h {
		t.Fatalf("decoded %+v, want %+v", back, h)
	}
	if _, err := decodeHandle(raw[:10]); err == nil {
		t.Fatal("short handle accepted")
	}
	raw[0] = 9
	if _, err := decodeHandle(raw); err == nil {
		t.Fatal("unknown handle version accepted")
	}
	if (nodeID{}).fileID() != 1 {
		t.Fatal("root fileid must be 1")
	}
}

// --- tree --------------------------------------------------------------

func fixtureItems() []*persistence.LibraryItem {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return []*persistence.LibraryItem{
		{ID: "movie:tt0111161:Shawshank.1994.2160p", ContentType: "movie", ContentID: "tt0111161", ImdbID: "tt0111161",
			ReleaseTitle: "The.Shawshank.Redemption.1994.2160p.BluRay-GRP", SizeBytes: 50 << 30, MediaFileName: "shawshank.mkv", MediaFileSize: 49 << 30, Status: "good", CreatedAt: created},
		{ID: "movie:tt0111161:Shawshank.1994.1080p", ContentType: "movie", ContentID: "tt0111161", ImdbID: "tt0111161",
			ReleaseTitle: "The.Shawshank.Redemption.1994.1080p.WEB-GRP", SizeBytes: 10 << 30, MediaFileName: "x.mp4", MediaFileSize: 9 << 30, Status: "good", CreatedAt: created.Add(time.Hour)},
		{ID: "series:tt0944947:1:2:GoT.S01E02", ContentType: "series", ContentID: "tt0944947", ImdbID: "tt0944947", TvdbID: "121361", Season: 1, Episode: 2,
			ReleaseTitle: "Game.of.Thrones.S01E02.1080p-GRP", SizeBytes: 3 << 30, MediaFileName: "got.mkv", MediaFileSize: 3 << 30, Status: "good", CreatedAt: created},
	}
}

// publishAll puts every fixture item in downloads/<release title>/<file>.
func publishAll(t *testing.T, s *Server, items []*persistence.LibraryItem) {
	t.Helper()
	for _, item := range items {
		if _, ok := s.PublishDownload(context.Background(), item.ReleaseTitle, item); !ok {
			t.Fatalf("publish %s", item.ID)
		}
	}
}

func TestBuildTreePlacesExportNodes(t *testing.T) {
	items := fixtureItems()
	now := time.Now()
	vfs := []persistence.ExportNode{
		{ID: 1, ParentID: 0, Name: "downloads", Kind: "dir", ModifiedAt: now},
		{ID: 2, ParentID: 0, Name: "media", Kind: "dir", ModifiedAt: now},
		{ID: 3, ParentID: 1, Name: "Job/With:Bad*Chars", Kind: "dir", ModifiedAt: now},
		{ID: 4, ParentID: 3, Name: "shawshank.mkv", Kind: "file", ItemID: items[0].ID, ModifiedAt: now},
		{ID: 5, ParentID: 3, Name: "gone.mkv", Kind: "file", ItemID: "movie:missing", ModifiedAt: now},
		{ID: 6, ParentID: 2, Name: "Iron Man (2008)", Kind: "dir", ModifiedAt: now},
		{ID: 7, ParentID: 6, Name: "Iron Man (2008).mkv", Kind: "file", ItemID: items[1].ID, ModifiedAt: now},
	}
	tr := buildTree(items, vfs, nil, now)
	if got := strings.Join(names(tr.root), "|"); got != "downloads|library|media" {
		t.Fatalf("root = %s", got) // media here is a client-made folder
	}
	// Rows aliased in the client's tree stay out of library/; the third
	// fixture row is nobody's and is laid out there.
	lib := tr.root.child("library")
	if got := strings.Join(names(lib), "|"); got != "Movies|Shows" {
		t.Fatalf("library = %s", got)
	}
	if n := len(lib.child("Movies").children); n != 0 {
		t.Fatalf("aliased movies must not be in library/, got %d folders", n)
	}
	if got := names(lib.child("Shows")); len(got) != 1 || !strings.Contains(got[0], "{imdb-tt0944947}") {
		t.Fatalf("library shows = %v", got)
	}
	job := tr.root.child("downloads").children[0]
	if job.name != "Job With Bad Chars" || job.vfsID != 3 {
		t.Fatalf("job dir = %q vfs=%d", job.name, job.vfsID)
	}
	if got := names(job); strings.Join(got, "|") != "shawshank.mkv" {
		t.Fatalf("alias to a missing row must be hidden: %v", got)
	}
	f := job.children[0]
	if f.size != 49<<30 || f.id != nodeIDFor(kindFile, vfsPath(4)) || f.item != items[0] {
		t.Fatalf("file node %+v", f)
	}
	movie := tr.root.child("media").child("Iron Man (2008)")
	if movie == nil || movie.children[0].size != 9<<30 {
		t.Fatal("media subtree")
	}
	for id, n := range tr.nodes {
		if n.id != id {
			t.Fatalf("node index out of step for %s", n.path)
		}
	}
	if buildTree(items, vfs, nil, now.Add(time.Hour)).gen != tr.gen {
		t.Fatal("verifier must depend on content only")
	}
	if buildTree(items, vfs[:4], nil, now).gen == tr.gen {
		t.Fatal("verifier must change with content")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		`a/b\c:d*e?f"g<h>i|j`: "a b c d e f g h i j",
		"  dots...  ":         "dots",
		"":                    "untitled",
		"tab\tand\x00ctrl":    "tabandctrl",
	}
	for in, want := range cases {
		if got := sanitizeName(in, 0); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("ä", 150)
	if got := sanitizeName(long, 100); len(got) > 100 || !strings.HasSuffix(got, "ä") {
		t.Fatalf("truncation cut a rune: %d bytes", len(got))
	}
}

func names(n *node) []string {
	out := make([]string, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c.name)
	}
	return out
}

// --- end to end --------------------------------------------------------

// TestMain keeps the portmapper, which every server now tries to bind, off
// port 111 for the whole package: privileged or taken on a developer machine.
func TestMain(m *testing.M) {
	portmapPort = 0
	os.Exit(m.Run())
}

type fakeStreams struct {
	streams map[string]*auth.Stream
}

func (f *fakeStreams) AuthenticateToken(token, adminUsername, adminToken string) (*auth.Stream, error) {
	if token != "" && token == adminToken {
		return &auth.Stream{Username: adminUsername, Token: adminToken}, nil
	}
	if s, ok := f.streams[token]; ok {
		return s, nil
	}
	return nil, errors.New("unknown token")
}

func (f *fakeStreams) GetAllStreams() []auth.Stream {
	out := make([]auth.Stream, 0, len(f.streams))
	for _, s := range f.streams {
		out = append(out, *s)
	}
	return out
}

type fakeLibrary struct {
	items []*persistence.LibraryItem
}

func (f *fakeLibrary) GetFilteredItems(_ string, _ string, _ bool, status string, offset, limit int) ([]*persistence.LibraryItem, int, error) {
	if status != persistence.LibraryStatusGood {
		return nil, 0, fmt.Errorf("unexpected status filter %q", status)
	}
	if offset >= len(f.items) {
		return nil, len(f.items), nil
	}
	end := min(offset+limit, len(f.items))
	return f.items[offset:end], len(f.items), nil
}

type memFile struct {
	*bytes.Reader
	closed *atomic.Int32
}

func (m *memFile) Close() error { m.closed.Add(1); return nil }

type testClient struct {
	t    *testing.T
	conn net.Conn
	xid  uint32
}

func dialTestServer(t *testing.T, s *Server) *testClient {
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &testClient{t: t, conn: conn, xid: 100}
}

func (c *testClient) call(prog, vers, proc uint32, args []byte) *decoder {
	d, accept := c.rawCall(prog, vers, proc, args)
	if accept != acceptSuccess {
		c.t.Fatalf("prog %d proc %d: accept_stat %d", prog, proc, accept)
	}
	return d
}

func (c *testClient) rawCall(prog, vers, proc uint32, args []byte) (*decoder, uint32) {
	c.xid++
	e := &encoder{}
	e.uint32(c.xid)
	e.uint32(msgCall)
	e.uint32(rpcVersion)
	e.uint32(prog)
	e.uint32(vers)
	e.uint32(proc)
	cred := &encoder{}
	cred.uint32(0)
	cred.string("plex-host")
	cred.uint32(1000)
	cred.uint32(1000)
	cred.uint32(0)
	e.uint32(authFlavorSys)
	e.opaque(cred.bytes())
	e.uint32(authFlavorNone)
	e.opaque(nil)
	e.buf = append(e.buf, args...)
	if _, err := c.conn.Write(frameRecord(e.bytes())); err != nil {
		c.t.Fatal(err)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reply, err := readRecord(c.conn, 4<<20)
	if err != nil {
		c.t.Fatal(err)
	}
	d := newDecoder(reply)
	if got := d.uint32(); got != c.xid {
		c.t.Fatalf("reply xid %d, want %d", got, c.xid)
	}
	if d.uint32() != msgReply || d.uint32() != replyAccepted {
		c.t.Fatal("reply not accepted")
	}
	d.uint32()
	d.opaque(maxAuthBody)
	return d, d.uint32()
}

func readFattr(d *decoder) (ftype uint32, size uint64, fileid uint64) {
	ftype = d.uint32()
	d.uint32()
	d.uint32()
	d.uint32()
	d.uint32()
	size = d.uint64()
	d.uint64()
	d.uint32()
	d.uint32()
	d.uint64()
	fileid = d.uint64()
	for i := 0; i < 3; i++ {
		d.uint32()
		d.uint32()
	}
	return
}

func readPostOpAttr(d *decoder) (present bool, ftype uint32, size uint64) {
	if !d.bool() {
		return false, 0, 0
	}
	ftype, size, _ = readFattr(d)
	return true, ftype, size
}

// newTestServer serves the fixture items published under downloads/, backed
// by content for every file.
func newTestServer(t *testing.T, content []byte, opens *atomic.Int32, closes *atomic.Int32) (*Server, *auth.Stream) {
	t.Helper()
	stream := &auth.Stream{Username: "living-room", Token: strings.Repeat("a", 64)}
	s := New(Options{
		Host:    "127.0.0.1",
		Port:    0,
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Admin:   func() (string, string) { return "admin", strings.Repeat("b", 64) },
		Library: &fakeLibrary{items: fixtureItems()},
		Export:  newMemExport(),
		Open: func(_ context.Context, st *auth.Stream, item *persistence.LibraryItem, _ string) (io.ReadSeekCloser, int64, error) {
			if st.Username != "living-room" {
				return nil, 0, errors.New("wrong stream")
			}
			if opens != nil {
				opens.Add(1)
			}
			return &memFile{Reader: bytes.NewReader(content), closed: closes}, int64(len(content)), nil
		},
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })
	publishAll(t, s, fixtureItems())
	return s, stream
}

func mountRoot(t *testing.T, c *testClient, token string) []byte {
	t.Helper()
	args := &encoder{}
	args.string("/" + token)
	d := c.call(progMount, mountVersion, mountProcMnt, args.bytes())
	if st := d.uint32(); st != mnt3OK {
		t.Fatalf("mount status %d", st)
	}
	return d.opaque(64)
}

// fileHandleFor is the handle of the published file for item, as a client
// would obtain it by LOOKUP.
func fileHandleFor(t *testing.T, c *testClient, rootFH []byte, item *persistence.LibraryItem) []byte {
	t.Helper()
	downloads := lookup(t, c, rootFH, "downloads", nf3Dir)
	job := lookup(t, c, downloads, item.ReleaseTitle, nf3Dir)
	return lookup(t, c, job, item.MediaFileName, nf3Reg)
}

func TestMountRefusesUnknownAndAdminTokens(t *testing.T) {
	var closes atomic.Int32
	s, _ := newTestServer(t, []byte("x"), nil, &closes)
	c := dialTestServer(t, s)
	for _, path := range []string{"/" + strings.Repeat("z", 64), "/" + strings.Repeat("b", 64), "/", "/a/b"} {
		args := &encoder{}
		args.string(path)
		d := c.call(progMount, mountVersion, mountProcMnt, args.bytes())
		if st := d.uint32(); st != mnt3ErrAcces {
			t.Errorf("mount %q: status %d, want EACCES", path, st)
		}
	}
	d := c.call(progMount, mountVersion, mountProcExport, nil)
	if d.bool() {
		t.Fatal("EXPORT must not list exports")
	}
}

func TestProgramAndVersionMismatches(t *testing.T) {
	var closes atomic.Int32
	s, _ := newTestServer(t, []byte("x"), nil, &closes)
	c := dialTestServer(t, s)
	if _, accept := c.rawCall(100021, 4, 0, nil); accept != acceptProgUnavail {
		t.Fatalf("lockd program: accept_stat %d, want PROG_UNAVAIL", accept)
	}
	d, accept := c.rawCall(progNFS, 2, 0, nil)
	if accept != acceptProgMismatch {
		t.Fatalf("NFSv2: accept_stat %d, want PROG_MISMATCH", accept)
	}
	if lo, hi := d.uint32(), d.uint32(); lo != 3 || hi != 3 {
		t.Fatalf("mismatch range %d..%d", lo, hi)
	}
	if _, accept := c.rawCall(progNFS, nfsVersion, 99, nil); accept != acceptProcUnavail {
		t.Fatalf("unknown proc: accept_stat %d", accept)
	}
}

func TestMountLookupReaddirAndRead(t *testing.T) {
	content := make([]byte, 3*1024*1024+123)
	for i := range content {
		content[i] = byte(i * 7)
	}
	var opens, closes atomic.Int32
	s, stream := newTestServer(t, content, &opens, &closes)
	c := dialTestServer(t, s)
	rootFH := mountRoot(t, c, stream.Token)
	rootH, err := decodeHandle(rootFH)
	if err != nil || rootH.kind != kindRoot || rootH.stream != streamKeyFor(stream.Token) {
		t.Fatalf("root handle %+v err %v", rootH, err)
	}

	d := c.call(progNFS, nfsVersion, nfsProcFsinfo, fh(rootFH))
	if d.uint32() != nfs3OK {
		t.Fatal("FSINFO failed")
	}
	readPostOpAttr(d)
	if rtmax := d.uint32(); rtmax != maxReadBytes {
		t.Fatalf("rtmax = %d", rtmax)
	}
	d = c.call(progNFS, nfsVersion, nfsProcPathconf, fh(rootFH))
	if d.uint32() != nfs3OK {
		t.Fatal("PATHCONF failed")
	}
	d = c.call(progNFS, nfsVersion, nfsProcGetattr, fh(rootFH))
	if d.uint32() != nfs3OK {
		t.Fatal("GETATTR root failed")
	}
	if ftype, _, fileid := readFattr(d); ftype != nf3Dir || fileid != 1 {
		t.Fatalf("root attrs type=%d fileid=%d", ftype, fileid)
	}

	items := fixtureItems()
	downloadsFH := lookup(t, c, rootFH, "downloads", nf3Dir)
	jobFH := lookup(t, c, downloadsFH, items[0].ReleaseTitle, nf3Dir)
	fileFH := lookup(t, c, jobFH, "shawshank.mkv", nf3Reg)

	args := &encoder{}
	args.opaque(jobFH)
	args.string("nope.mkv")
	d = c.call(progNFS, nfsVersion, nfsProcLookup, args.bytes())
	if st := d.uint32(); st != nfs3ErrNoEnt {
		t.Fatalf("missing lookup status %d", st)
	}
	if present, ftype, _ := readPostOpAttr(d); !present || ftype != nf3Dir {
		t.Fatal("ENOENT reply should carry the directory's attributes")
	}

	// READDIRPLUS downloads/: ".", "..", three job folders.
	args = &encoder{}
	args.opaque(downloadsFH)
	args.uint64(0)
	args.fixedOpaque(make([]byte, 8))
	args.uint32(8192)
	args.uint32(32768)
	d = c.call(progNFS, nfsVersion, nfsProcReaddirplus, args.bytes())
	if d.uint32() != nfs3OK {
		t.Fatal("READDIRPLUS failed")
	}
	readPostOpAttr(d)
	verf := d.fixedOpaque(8)
	var listed []string
	var lastCookie uint64
	for d.bool() {
		d.uint64()
		listed = append(listed, d.string(maxFileName))
		lastCookie = d.uint64()
		readPostOpAttr(d)
		if d.bool() {
			if _, err := decodeHandle(d.opaque(64)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !d.bool() {
		t.Fatal("expected eof")
	}
	if len(listed) != 5 || listed[0] != "." || listed[1] != ".." || lastCookie != 5 {
		t.Fatalf("listing = %v cookie=%d", listed, lastCookie)
	}

	// Paged READDIR with a tiny count, continued by cookie.
	args = &encoder{}
	args.opaque(downloadsFH)
	args.uint64(0)
	args.fixedOpaque(verf)
	args.uint32(200)
	d = c.call(progNFS, nfsVersion, nfsProcReaddir, args.bytes())
	if d.uint32() != nfs3OK {
		t.Fatal("READDIR failed")
	}
	readPostOpAttr(d)
	d.fixedOpaque(8)
	page := 0
	var cookie uint64
	for d.bool() {
		d.uint64()
		d.string(maxFileName)
		cookie = d.uint64()
		page++
	}
	if d.bool() || page == 0 || page >= 5 {
		t.Fatalf("first page should be partial: %d entries", page)
	}
	args = &encoder{}
	args.opaque(downloadsFH)
	args.uint64(cookie)
	args.fixedOpaque(verf)
	args.uint32(1 << 16)
	d = c.call(progNFS, nfsVersion, nfsProcReaddir, args.bytes())
	if d.uint32() != nfs3OK {
		t.Fatal("READDIR continuation failed")
	}
	readPostOpAttr(d)
	d.fixedOpaque(8)
	rest := 0
	for d.bool() {
		d.uint64()
		d.string(maxFileName)
		d.uint64()
		rest++
	}
	if !d.bool() || page+rest != 5 {
		t.Fatalf("pages do not add up: %d + %d", page, rest)
	}
	args = &encoder{}
	args.opaque(downloadsFH)
	args.uint64(2)
	args.fixedOpaque([]byte{9, 9, 9, 9, 9, 9, 9, 9})
	args.uint32(1 << 16)
	d = c.call(progNFS, nfsVersion, nfsProcReaddir, args.bytes())
	if st := d.uint32(); st != nfs3ErrBadCookie {
		t.Fatalf("stale verifier status %d", st)
	}

	d = c.call(progNFS, nfsVersion, nfsProcGetattr, fh(fileFH))
	d.uint32()
	if ftype, size, _ := readFattr(d); ftype != nf3Reg || size != 49<<30 {
		t.Fatalf("file attrs type=%d size=%d", ftype, size)
	}

	got := make([]byte, len(content))
	for _, off := range []int{2 * maxReadBytes, 0, maxReadBytes, 3 * maxReadBytes} {
		args = &encoder{}
		args.opaque(fileFH)
		args.uint64(uint64(off))
		args.uint32(maxReadBytes)
		d = c.call(progNFS, nfsVersion, nfsProcRead, args.bytes())
		if st := d.uint32(); st != nfs3OK {
			t.Fatalf("READ at %d: status %d", off, st)
		}
		readPostOpAttr(d)
		count := d.uint32()
		eof := d.bool()
		data := d.opaque(maxReadBytes)
		if int(count) != len(data) {
			t.Fatalf("count %d but %d bytes", count, len(data))
		}
		if wantEOF := off+len(data) >= len(content); eof != wantEOF {
			t.Fatalf("READ at %d: eof=%v want %v", off, eof, wantEOF)
		}
		copy(got[off:], data)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("reassembled content differs")
	}
	if opens.Load() != 1 {
		t.Fatalf("file opened %d times, want 1", opens.Load())
	}

	args = &encoder{}
	args.opaque(fileFH)
	args.uint64(uint64(len(content) + 10))
	args.uint32(100)
	d = c.call(progNFS, nfsVersion, nfsProcRead, args.bytes())
	d.uint32()
	readPostOpAttr(d)
	if count, eof := d.uint32(), d.bool(); count != 0 || !eof {
		t.Fatalf("read past end: count=%d eof=%v", count, eof)
	}

	args = &encoder{}
	args.opaque(jobFH)
	args.uint64(0)
	args.uint32(10)
	d = c.call(progNFS, nfsVersion, nfsProcRead, args.bytes())
	if st := d.uint32(); st != nfs3ErrIsDir {
		t.Fatalf("read dir status %d", st)
	}
	args = &encoder{}
	args.opaque(fileFH)
	args.uint64(0)
	args.uint32(3)
	args.uint32(0)
	args.opaque([]byte("abc"))
	d = c.call(progNFS, nfsVersion, nfsProcWrite, args.bytes())
	if st := d.uint32(); st != nfs3ErrROFS {
		t.Fatalf("write status %d", st)
	}
	if d.bool() {
		t.Fatal("no before-attributes expected")
	}
	if present, _, _ := readPostOpAttr(d); !present {
		t.Fatal("ROFS reply should still describe the file")
	}

	fileNode, _ := decodeHandle(fileFH)
	gone := handle{kind: kindFile, stream: streamKeyFor("other"), node: fileNode.node}
	args = &encoder{}
	args.opaque(gone.encode())
	args.uint64(0)
	args.uint32(10)
	d = c.call(progNFS, nfsVersion, nfsProcRead, args.bytes())
	if st := d.uint32(); st != nfs3ErrStale {
		t.Fatalf("unknown stream status %d", st)
	}
	args = &encoder{}
	args.opaque([]byte("not a handle"))
	d = c.call(progNFS, nfsVersion, nfsProcGetattr, args.bytes())
	if st := d.uint32(); st != nfs3ErrBadHandle {
		t.Fatalf("bad handle status %d", st)
	}

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("file closed %d times, want 1", closes.Load())
	}
}

func TestReadReopensAfterStreamFailure(t *testing.T) {
	content := bytes.Repeat([]byte("abcd"), 1024)
	var opens, closes atomic.Int32
	failNext := &atomic.Bool{}
	stream := &auth.Stream{Username: "living-room", Token: strings.Repeat("a", 64)}
	s := New(Options{
		Host:    "127.0.0.1",
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Library: &fakeLibrary{items: fixtureItems()[:1]},
		Export:  newMemExport(),
		Open: func(context.Context, *auth.Stream, *persistence.LibraryItem, string) (io.ReadSeekCloser, int64, error) {
			opens.Add(1)
			return &failingFile{memFile: &memFile{Reader: bytes.NewReader(content), closed: &closes}, fail: failNext}, int64(len(content)), nil
		},
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })
	publishAll(t, s, fixtureItems()[:1])
	c := dialTestServer(t, s)
	rootFH := mountRoot(t, c, stream.Token)
	fileFH := fileHandleFor(t, c, rootFH, fixtureItems()[0])

	read := func(off int) (uint32, []byte) {
		args := &encoder{}
		args.opaque(fileFH)
		args.uint64(uint64(off))
		args.uint32(16)
		d := c.call(progNFS, nfsVersion, nfsProcRead, args.bytes())
		st := d.uint32()
		readPostOpAttr(d)
		if st != nfs3OK {
			return st, nil
		}
		d.uint32()
		d.bool()
		return st, d.opaque(16)
	}
	if st, data := read(0); st != nfs3OK || string(data) != "abcdabcdabcdabcd" {
		t.Fatalf("first read: %d %q", st, data)
	}
	failNext.Store(true)
	if st, _ := read(16); st != nfs3ErrIO {
		t.Fatalf("failed read should be EIO, got %d", st)
	}
	if closes.Load() != 1 {
		t.Fatal("a failed stream must be closed, not kept")
	}
	if st, data := read(16); st != nfs3OK || string(data) != "abcdabcdabcdabcd" {
		t.Fatalf("read after failure: %d %q", st, data)
	}
	if opens.Load() != 2 {
		t.Fatalf("expected a reopen, opens=%d", opens.Load())
	}
}

type failingFile struct {
	*memFile
	fail *atomic.Bool
}

func (f *failingFile) Read(p []byte) (int, error) {
	if f.fail.Swap(false) {
		return 0, errors.New("provider went away")
	}
	return f.memFile.Read(p)
}

func fh(raw []byte) []byte {
	e := &encoder{}
	e.opaque(raw)
	return e.bytes()
}

func lookup(t *testing.T, c *testClient, dirFH []byte, name string, wantType uint32) []byte {
	t.Helper()
	args := &encoder{}
	args.opaque(dirFH)
	args.string(name)
	d := c.call(progNFS, nfsVersion, nfsProcLookup, args.bytes())
	if st := d.uint32(); st != nfs3OK {
		t.Fatalf("LOOKUP %q: status %d", name, st)
	}
	objFH := d.opaque(64)
	present, ftype, _ := readPostOpAttr(d)
	if !present || ftype != wantType {
		t.Fatalf("LOOKUP %q: attrs present=%v type=%d", name, present, ftype)
	}
	if present, _, _ := readPostOpAttr(d); !present {
		t.Fatalf("LOOKUP %q: no dir attrs", name)
	}
	return objFH
}

func TestPortmapperAnswersGetportOverTCPAndUDP(t *testing.T) {

	stream := &auth.Stream{Username: "pc", Token: strings.Repeat("a", 64)}
	s := New(Options{
		Host:    "127.0.0.1",
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Library: &fakeLibrary{},
		Export:  newMemExport(),
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })
	if st := s.Status(); !st.Portmapper.Listening {
		t.Fatalf("portmapper status = %+v", st.Portmapper)
	}
	served := uint32(s.servedPort())

	getport := func(c *testClient, prog, prot uint32) uint32 {
		args := &encoder{}
		args.uint32(prog)
		args.uint32(3)
		args.uint32(prot)
		args.uint32(0)
		d := c.call(progPortmap, portmapVersion, pmapProcGetport, args.bytes())
		return d.uint32()
	}
	s.mu.Lock()
	tcpAddr := s.pmapTCP.Addr().String()
	udpAddr := s.pmapUDP.LocalAddr().String()
	s.mu.Unlock()
	conn, err := net.Dial("tcp", tcpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &testClient{t: t, conn: conn, xid: 500}
	if got := getport(c, progNFS, ipprotoTCP); got != served {
		t.Fatalf("GETPORT nfs/tcp = %d, want %d", got, served)
	}
	if got := getport(c, progMount, ipprotoTCP); got != served {
		t.Fatalf("GETPORT mount/tcp = %d, want %d", got, served)
	}
	if got := getport(c, progNFS, ipprotoUDP); got != 0 {
		t.Fatalf("GETPORT nfs/udp = %d, want 0", got)
	}
	if got := getport(c, 100021, ipprotoTCP); got != 0 {
		t.Fatalf("GETPORT lockd = %d, want 0", got)
	}
	args := &encoder{}
	for i := 0; i < 4; i++ {
		args.uint32(1)
	}
	if d := c.call(progPortmap, portmapVersion, pmapProcSet, args.bytes()); d.bool() {
		t.Fatal("SET must be refused")
	}
	if _, accept := c.rawCall(progPortmap, 4, pmapProcNull, nil); accept != acceptProgMismatch {
		t.Fatalf("rpcbind v4: accept_stat %d, want PROG_MISMATCH", accept)
	}

	udp, err := net.Dial("udp", udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	e := &encoder{}
	e.uint32(77)
	e.uint32(msgCall)
	e.uint32(rpcVersion)
	e.uint32(progPortmap)
	e.uint32(portmapVersion)
	e.uint32(pmapProcGetport)
	e.uint32(authFlavorNone)
	e.opaque(nil)
	e.uint32(authFlavorNone)
	e.opaque(nil)
	e.uint32(progMount)
	e.uint32(mountVersion)
	e.uint32(ipprotoTCP)
	e.uint32(0)
	if _, err := udp.Write(e.bytes()); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	n, err := udp.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	d := newDecoder(buf[:n])
	if d.uint32() != 77 || d.uint32() != msgReply || d.uint32() != replyAccepted {
		t.Fatal("udp reply header")
	}
	d.uint32()
	d.opaque(maxAuthBody)
	if d.uint32() != acceptSuccess {
		t.Fatal("udp reply not success")
	}
	if got := d.uint32(); got != served {
		t.Fatalf("udp GETPORT = %d, want %d", got, served)
	}
}

func TestSlowOpenAnswersJukeboxThenData(t *testing.T) {
	savedWait := openWait
	openWait = 300 * time.Millisecond
	t.Cleanup(func() { openWait = savedWait })

	content := []byte("0123456789abcdef")
	release := make(chan struct{})
	var opens, closes atomic.Int32
	stream := &auth.Stream{Username: "pc", Token: strings.Repeat("a", 64)}
	s := New(Options{
		Host:    "127.0.0.1",
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Library: &fakeLibrary{items: fixtureItems()[:1]},
		Export:  newMemExport(),
		Open: func(ctx context.Context, _ *auth.Stream, _ *persistence.LibraryItem, _ string) (io.ReadSeekCloser, int64, error) {
			opens.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
			return &memFile{Reader: bytes.NewReader(content), closed: &closes}, int64(len(content)), nil
		},
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })
	publishAll(t, s, fixtureItems()[:1])
	c := dialTestServer(t, s)
	rootFH := mountRoot(t, c, stream.Token)
	fileFH := fileHandleFor(t, c, rootFH, fixtureItems()[0])
	read := func() (uint32, []byte) {
		args := &encoder{}
		args.opaque(fileFH)
		args.uint64(0)
		args.uint32(8)
		d := c.call(progNFS, nfsVersion, nfsProcRead, args.bytes())
		st := d.uint32()
		readPostOpAttr(d)
		if st != nfs3OK {
			return st, nil
		}
		d.uint32()
		d.bool()
		return st, d.opaque(8)
	}
	start := time.Now()
	if st, _ := read(); st != nfs3ErrJukebox {
		t.Fatalf("read during open: status %d, want JUKEBOX", st)
	}
	if time.Since(start) > 2*openWait+time.Second {
		t.Fatal("JUKEBOX took too long to come back")
	}
	if st, _ := read(); st != nfs3ErrJukebox {
		t.Fatal("second read during open should also be JUKEBOX")
	}
	if opens.Load() != 1 {
		t.Fatalf("open started %d times, want 1", opens.Load())
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, data := read()
		if st == nfs3OK {
			if string(data) != "01234567" {
				t.Fatalf("data %q", data)
			}
			break
		}
		if st != nfs3ErrJukebox || time.Now().After(deadline) {
			t.Fatalf("read after open: status %d", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ctxBoundFile fails every read once the context it was opened under is
// done, which is how the real playback stream behaves.
type ctxBoundFile struct {
	*memFile
	ctx context.Context
}

func (f *ctxBoundFile) Read(p []byte) (int, error) {
	if f.ctx.Err() != nil {
		return 0, io.ErrUnexpectedEOF
	}
	return f.memFile.Read(p)
}

func TestOpenContextOutlivesTheOpen(t *testing.T) {
	content := bytes.Repeat([]byte("z"), 4096)
	var closes atomic.Int32
	stream := &auth.Stream{Username: "pc", Token: strings.Repeat("a", 64)}
	s := New(Options{
		Host:    "127.0.0.1",
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Library: &fakeLibrary{items: fixtureItems()[:1]},
		Export:  newMemExport(),
		Open: func(ctx context.Context, _ *auth.Stream, _ *persistence.LibraryItem, _ string) (io.ReadSeekCloser, int64, error) {
			return &ctxBoundFile{memFile: &memFile{Reader: bytes.NewReader(content), closed: &closes}, ctx: ctx}, int64(len(content)), nil
		},
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })
	publishAll(t, s, fixtureItems()[:1])
	c := dialTestServer(t, s)
	rootFH := mountRoot(t, c, stream.Token)
	fileFH := fileHandleFor(t, c, rootFH, fixtureItems()[0])
	for _, off := range []uint64{0, 1024, 2048} {
		args := &encoder{}
		args.opaque(fileFH)
		args.uint64(off)
		args.uint32(1024)
		d := c.call(progNFS, nfsVersion, nfsProcRead, args.bytes())
		if st := d.uint32(); st != nfs3OK {
			t.Fatalf("read at %d: status %d", off, st)
		}
		readPostOpAttr(d)
		count := d.uint32()
		eof := d.bool()
		if count != 1024 || eof != (off == 3072) {
			t.Fatalf("read at %d: count=%d eof=%v — the stream was cancelled under the reader", off, count, eof)
		}
	}
	if closes.Load() != 0 {
		t.Fatalf("stream closed %d times while still in use", closes.Load())
	}
}

func TestLegacyRowReportsMeasuredSizeOnLookup(t *testing.T) {
	content := bytes.Repeat([]byte("m"), 3000)
	var closes, opens atomic.Int32
	legacy := &persistence.LibraryItem{ID: "movie:tt1:Legacy.2008-GRP", ContentType: "movie", ContentID: "tt1", ImdbID: "tt1",
		ReleaseTitle: "Legacy.2008-GRP", SizeBytes: 5000, MediaFileName: "", MediaFileSize: 0, Status: "good", CreatedAt: time.Now()}
	stream := &auth.Stream{Username: "pc", Token: strings.Repeat("a", 64)}
	s := New(Options{
		Host:    "127.0.0.1",
		Streams: &fakeStreams{streams: map[string]*auth.Stream{stream.Token: stream}},
		Library: &fakeLibrary{items: []*persistence.LibraryItem{legacy}},
		Export:  newMemExport(),
		Open: func(context.Context, *auth.Stream, *persistence.LibraryItem, string) (io.ReadSeekCloser, int64, error) {
			opens.Add(1)
			return &memFile{Reader: bytes.NewReader(content), closed: &closes}, int64(len(content)), nil
		},
	})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Stop() })
	folder, ok := s.PublishDownload(context.Background(), "Legacy.2008-GRP", legacy)
	if !ok || folder != "/downloads/Legacy.2008-GRP" {
		t.Fatalf("publish: %q %v", folder, ok)
	}
	c := dialTestServer(t, s)
	rootFH := mountRoot(t, c, stream.Token)
	downloads := lookup(t, c, rootFH, "downloads", nf3Dir)
	jobFH := lookup(t, c, downloads, "Legacy.2008-GRP", nf3Dir)
	job := s.currentTree().root.child("downloads").children[0]
	if job.children[0].size != 5000 || job.children[0].name != "Legacy.2008-GRP.mkv" {
		t.Fatalf("precondition: %q size %d", job.children[0].name, job.children[0].size)
	}
	args := &encoder{}
	args.opaque(jobFH)
	args.string("Legacy.2008-GRP.mkv")
	d := c.call(progNFS, nfsVersion, nfsProcLookup, args.bytes())
	if d.uint32() != nfs3OK {
		t.Fatal("lookup failed")
	}
	fileFH := d.opaque(64)
	if present, _, size := readPostOpAttr(d); !present || size != 3000 {
		t.Fatalf("LOOKUP must report the measured size: present=%v size=%d", present, size)
	}
	if opens.Load() != 1 {
		t.Fatalf("expected one measuring open, got %d", opens.Load())
	}
	d = c.call(progNFS, nfsVersion, nfsProcGetattr, fh(fileFH))
	d.uint32()
	if _, size, _ := readFattr(d); size != 3000 {
		t.Fatalf("GETATTR size = %d", size)
	}
	args = &encoder{}
	args.opaque(fileFH)
	args.uint64(2990)
	args.uint32(100)
	d = c.call(progNFS, nfsVersion, nfsProcRead, args.bytes())
	d.uint32()
	readPostOpAttr(d)
	if count, eof := d.uint32(), d.bool(); count != 10 || !eof {
		t.Fatalf("tail read count=%d eof=%v", count, eof)
	}
	if opens.Load() != 1 {
		t.Fatalf("read should reuse the open, opens=%d", opens.Load())
	}
}
