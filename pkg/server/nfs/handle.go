package nfs

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// A file handle is the server's name for a file or directory; the client
// treats it as opaque and hands it back on every call. NFSv3 allows 64 bytes.
// Ours is 26 and carries everything a call needs, so no table has to survive
// a restart for a mount to keep working:
//
//	[0]     version, so a later layout can refuse an old handle as STALE
//	[1]     kind: root, directory or file
//	[2:10]  stream key — the first 8 bytes of SHA-256 over the stream token
//	[10:26] node id — the first 16 bytes of SHA-256 over the node's path
//
// The stream key is what ties every call back to the stream whose token was
// in the mount path: NFS calls carry no mount context of their own, and the
// stream is what decides which providers a read goes through and whose
// connection budget it spends. A regenerated token therefore strands the
// handles minted under the old one, which the client sees as ESTALE until it
// remounts — the same thing a reinstalled addon goes through.

const (
	handleVersion = 1
	handleLen     = 26

	kindRoot = 1
	kindDir  = 2
	kindFile = 3
)

type streamKey [8]byte
type nodeID [16]byte

type handle struct {
	kind   byte
	stream streamKey
	node   nodeID
}

var errBadHandle = errors.New("nfs: malformed file handle")

func (h handle) encode() []byte {
	out := make([]byte, handleLen)
	out[0] = handleVersion
	out[1] = h.kind
	copy(out[2:10], h.stream[:])
	copy(out[10:26], h.node[:])
	return out
}

func decodeHandle(b []byte) (handle, error) {
	if len(b) != handleLen || b[0] != handleVersion {
		return handle{}, errBadHandle
	}
	h := handle{kind: b[1]}
	switch h.kind {
	case kindRoot, kindDir, kindFile:
	default:
		return handle{}, errBadHandle
	}
	copy(h.stream[:], b[2:10])
	copy(h.node[:], b[10:26])
	return h, nil
}

func streamKeyFor(token string) streamKey {
	sum := sha256.Sum256([]byte("stream:" + token))
	var k streamKey
	copy(k[:], sum[:8])
	return k
}

// nodeIDFor names a tree node by its full path, which is what makes the
// handle stable: the same library row under the same stream yields the same
// node id after any restart or tree rebuild.
func nodeIDFor(kind byte, path string) nodeID {
	var prefix string
	switch kind {
	case kindDir:
		prefix = "dir:"
	case kindFile:
		prefix = "file:"
	default:
		return nodeID{}
	}
	sum := sha256.Sum256([]byte(prefix + path))
	var id nodeID
	copy(id[:], sum[:16])
	return id
}

// fileID is the 64-bit inode number a node reports. The root is 1, as on most
// filesystems; everything else is the leading half of its node id.
func (id nodeID) fileID() uint64 {
	if id == (nodeID{}) {
		return 1
	}
	v := binary.BigEndian.Uint64(id[:8])
	if v <= 1 {
		v += 2
	}
	return v
}
