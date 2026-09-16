// Package rar reads RAR archives well enough to stream stored media out of
// them, and no further.
//
// # Provenance
//
// This began as nwaples/rardecode by way of javi11/rardecode/v2, and keeps
// their BSD licence — see the LICENSE file beside this one. It was vendored
// under third_party/ while it was still someone else's library that we
// patched; by the time the patches included a seekable CBC decryptor, a
// tolerant multi-volume lister and a parallel volume reader, calling it a
// dependency was a fiction. It lives here now, under the layer that uses it.
//
// # What it does not do
//
// It does not decompress. Streaming maps a player's byte range straight onto
// the packed bytes inside the archive, which is only the same thing when the
// file is stored; a compressed release cannot be streamed however well it
// decompresses, and the pipeline turns one away on its header long before any
// data is read. The decoders, the PPMd model, the sliding windows and the
// filter VM were therefore unreachable, and are gone.
//
// Compression is still recognised. A file header records which decoder its
// data would need, Reader.Next still reports that file with Stored false, and
// only reading its data fails. That ordering matters: a compressed release is
// healthy — its articles are fine — so callers tell it apart from a damaged
// archive by the header, and report it as unstreamable instead of reporting
// the release bad to a community database.
package rar
