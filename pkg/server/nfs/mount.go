package nfs

import (
	"streamnzb/pkg/core/logger"
)

// MOUNT version 3 (RFC 1813 appendix I). The client calls MNT with a path
// and gets back the root file handle it will use for everything after; here
// the path is a stream token and the handle is minted for that stream.

const (
	mountProcNull    = 0
	mountProcMnt     = 1
	mountProcDump    = 2
	mountProcUmnt    = 3
	mountProcUmntAll = 4
	mountProcExport  = 5

	mnt3OK       = 0
	mnt3ErrAcces = 13

	// maxMountPath bounds the dirpath argument. A token is 64 characters.
	maxMountPath = 1024
)

func (s *Server) mountProc(c *conn, call *rpcCall) []byte {
	switch call.proc {
	case mountProcNull:
		return replyHeader(call.xid, acceptSuccess).bytes()

	case mountProcMnt:
		dirpath := call.args.string(maxMountPath)
		if call.args.err() != nil {
			return replyHeader(call.xid, acceptGarbageArgs).bytes()
		}
		e := replyHeader(call.xid, acceptSuccess)
		stream, err := s.authenticateMount(dirpath)
		if err != nil {
			// The path is the credential: never echo it, and say the same
			// thing for an unknown token as for a refused identity.
			logger.Debug("NFS mount refused", "client", c.addr, "err", err)
			e.uint32(mnt3ErrAcces)
			return e.bytes()
		}
		root := handle{kind: kindRoot, stream: streamKeyFor(stream.Token)}
		e.uint32(mnt3OK)
		e.opaque(root.encode())
		// auth_flavors<>: what the server accepts on NFS calls. Both, since
		// neither is checked — the mount path already decided.
		e.uint32(2)
		e.uint32(authFlavorSys)
		e.uint32(authFlavorNone)
		logger.Info("NFS export mounted", "stream", stream.Username, "client", c.addr)
		return e.bytes()

	case mountProcDump:
		// mountlist: an empty list. Who has mounted what is not the business
		// of whoever asks.
		e := replyHeader(call.xid, acceptSuccess)
		e.bool(false)
		return e.bytes()

	case mountProcUmnt:
		_ = call.args.string(maxMountPath)
		logger.Debug("NFS export unmounted", "client", c.addr)
		return replyHeader(call.xid, acceptSuccess).bytes()

	case mountProcUmntAll:
		return replyHeader(call.xid, acceptSuccess).bytes()

	case mountProcExport:
		// exports: an empty list. Listing the exports would list the tokens.
		e := replyHeader(call.xid, acceptSuccess)
		e.bool(false)
		return e.bytes()
	}
	return replyHeader(call.xid, acceptProcUnavail).bytes()
}
