package nfs

import (
	"encoding/binary"
	"hash/fnv"
	"path"
	"sort"
	"strings"
	"time"

	"streamnzb/pkg/core/persistence"
)

// The export is a view of the export store, not a store of its own: a tree
// of names the client chose, whose files are aliases to library rows.
//
//	downloads/                        ← finished download-client jobs land here
//	  Iron.Man.2008.UHD.BluRay-GRP/
//	    Iron.Man.2008.UHD.BluRay-GRP.mkv
//	movies/                           ← whatever the *arr makes; it names things
//	tv/
//	library/                          ← generated, read-only: see library_tree.go
//
// StreamNZB does not name or sort what an *arr owns. Radarr and Sonarr import
// a job by renaming its file out of downloads/ into their own folders beside
// it, and Plex scans those. Rows nobody organises — played through Stremio,
// never imported — are laid out for a scanner under library/. Everything is
// writable except the downloads folder's name and place and the generated
// library folder.

const (
	// defaultMediaExt is used when a row predates the media file name column.
	defaultMediaExt = ".mkv"

	// maxNameBytes keeps every component under NAME_MAX on the client side.
	maxNameBytes = 200
)

type node struct {
	id     nodeID
	kind   byte
	name   string
	path   string
	parent *node
	// children is sorted by name; byName indexes it.
	children []*node
	byName   map[string]*node
	// item is set on alias files; scratch is set on files the client wrote
	// itself and holds their content.
	item    *persistence.LibraryItem
	scratch *scratchFile
	size    int64
	mtime   time.Time
	// vfsID is the export-store row behind the node; zero for the root, which
	// the store addresses as parent 0, and for the generated library folder.
	vfsID int64
}

// scratchFile is the content of a client-written file.
type scratchFile struct {
	data []byte
}

// generated reports a read-only node: part of library/, built from the
// library rather than chosen by a client.
func (n *node) generated() bool { return n.kind != kindRoot && n.vfsID == 0 }

func (n *node) isDir() bool { return n.kind != kindFile }

func (n *node) child(name string) *node {
	if n.byName == nil {
		return nil
	}
	return n.byName[name]
}

func (n *node) addChild(c *node) {
	if n.byName == nil {
		n.byName = make(map[string]*node)
	}
	n.byName[c.name] = c
	n.children = append(n.children, c)
	c.parent = n
}

// tree is one build of the export. nodes resolves a handle's node id; gen is
// the READDIR cookie verifier, derived from the content so it changes only
// when the listing itself does.
type tree struct {
	root  *node
	nodes map[nodeID]*node
	gen   uint64
	built time.Time
}

func (t *tree) lookup(id nodeID) *node {
	if t == nil {
		return nil
	}
	if id == (nodeID{}) {
		return t.root
	}
	return t.nodes[id]
}

// buildTree grafts the export store's tree onto the root, then lays out the
// rows it does not reference under library/. items are the playable library
// rows; an alias to a row not among them is left out, so a release that went
// bad disappears from the listing rather than failing every read. Nodes come
// in id order and a parent always has a lower id than its children (it
// existed first), so one pass places everything.
func buildTree(items []*persistence.LibraryItem, vfs []persistence.ExportNode, titles titleFunc, now time.Time) *tree {
	root := &node{kind: kindRoot, name: "", path: "/"}
	t := &tree{root: root, nodes: map[nodeID]*node{}, built: now}
	byItem := make(map[string]*persistence.LibraryItem, len(items))
	for _, item := range items {
		if item != nil {
			byItem[item.ID] = item
		}
	}
	gen := fnv.New64a()
	aliased := make(map[string]bool)
	placed := map[int64]*node{0: root}
	pending := vfs
	for progress := true; progress && len(pending) > 0; {
		progress = false
		var rest []persistence.ExportNode
		for _, vn := range pending {
			parent, ok := placed[vn.ParentID]
			if !ok {
				rest = append(rest, vn)
				continue
			}
			progress = true
			name := sanitizeName(vn.Name, maxNameBytes)
			if parent.child(name) != nil {
				continue
			}
			var n *node
			if vn.IsDir() {
				n = &node{kind: kindDir, name: name, path: path.Join(parent.path, name), mtime: vn.ModifiedAt, vfsID: vn.ID}
				n.id = nodeIDFor(kindDir, vfsPath(vn.ID))
			} else if vn.IsScratch() {
				n = &node{kind: kindFile, name: name, path: path.Join(parent.path, name), scratch: &scratchFile{data: vn.Data}, size: int64(len(vn.Data)), mtime: vn.ModifiedAt, vfsID: vn.ID}
				n.id = nodeIDFor(kindFile, vfsPath(vn.ID))
			} else {
				item := byItem[vn.ItemID]
				if item == nil {
					continue
				}
				n = &node{kind: kindFile, name: name, path: path.Join(parent.path, name), item: item, size: fileSize(item), mtime: vn.ModifiedAt, vfsID: vn.ID}
				n.id = nodeIDFor(kindFile, vfsPath(vn.ID))
				aliased[item.ID] = true
			}
			parent.addChild(n)
			t.nodes[n.id] = n
			placed[vn.ID] = n
			_, _ = gen.Write([]byte(n.path))
			_, _ = gen.Write([]byte{0})
		}
		pending = rest
	}
	if root.child(libraryDirName) == nil {
		addLibraryTree(t, root, items, aliased, titles, gen)
	}
	t.gen = gen.Sum64()
	if t.gen == 0 {
		t.gen = 1
	}
	finishDir(root, now)
	return t
}

// finishDir sorts every directory and gives it the newest mtime beneath it,
// so a scanner watching directory times notices additions.
func finishDir(n *node, now time.Time) {
	if !n.isDir() {
		return
	}
	sort.Slice(n.children, func(i, j int) bool { return n.children[i].name < n.children[j].name })
	latest := n.mtime
	for _, c := range n.children {
		finishDir(c, now)
		if c.mtime.After(latest) {
			latest = c.mtime
		}
	}
	if latest.IsZero() {
		latest = now
	}
	n.mtime = latest
}

func fileSize(item *persistence.LibraryItem) int64 {
	if item.MediaFileSize > 0 {
		return item.MediaFileSize
	}
	return item.SizeBytes
}

// sanitizeName makes a path component every client filesystem accepts: no
// separators or the characters Windows refuses, no control characters, no
// leading or trailing dots and spaces, and bounded in bytes without cutting
// a multibyte rune in half.
func sanitizeName(s string, maxBytes int) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			r = ' '
		}
		if r == ' ' {
			if lastSpace {
				continue
			}
			lastSpace = true
		} else {
			lastSpace = false
		}
		b.WriteRune(r)
	}
	out := strings.Trim(b.String(), " .")
	if maxBytes > 0 && len(out) > maxBytes {
		cut := maxBytes
		for cut > 0 && !isRuneStart(out[cut]) {
			cut--
		}
		out = strings.TrimRight(out[:cut], " .")
	}
	if out == "" {
		return "untitled"
	}
	return out
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// cookieVerf renders the tree generation as the 8-byte verifier READDIR
// hands back with every page.
func (t *tree) cookieVerf() []byte {
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], t.gen)
	return v[:]
}
