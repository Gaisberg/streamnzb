# Obfuscated releases

Some posters strip a release of its filenames before uploading it. In the mildest
form only the NZB subject is scrambled; in the harshest — a *fully obfuscated*
post — every file is renamed to a random hash with no extension at all, so the
NZB looks like this:

```
[01/13] - "aa11bb22cc33dd44.par2" yEnc (1/1)
[03/13] - "3f8a91b2c7d6e5f4" yEnc (1/100)
[04/13] - "9c0d1e2f3a4b5c6d" yEnc (1/100)
```

Nothing in those subjects says which file is the media, which are RAR volumes, or
which are PAR2 recovery data. StreamNZB recovers that from the release's own
bytes, so these releases search, validate and play like any other. There is
nothing to configure.

## How a name is recovered

Three sources are tried in order, most trustworthy first:

1. **PAR2 file descriptions.** A PAR2 recovery set records every file of the
   release by name, length and the MD5 of its first 16 KiB. That hash identifies
   each NZB file outright, so a real filename is matched by content — never by
   guessing at position or size. Posters who obfuscate only the subject leave the
   original names here.
2. **yEnc headers.** Every article of a file repeats the `=ybegin name=` the
   uploader wrote. When the PAR2 set is missing or was built after the rename,
   this is often still the real name.
3. **Content signatures.** When both come back empty the file's first bytes still
   say what it *is* — PAR2, RAR, 7z, Matroska, MP4 or AVI. RAR5 volumes also
   state their volume number in their own header, which puts an anonymous set
   back in order; RAR4 volumes state only which one is first, so the rest keep
   the order the poster uploaded them in.

Recovered names are used for routing (which unpacker runs, which volume opens the
set) and for display, so a stream that used to show a hash now shows the real
filename where one was recoverable.

## What this changes in practice

- A fully obfuscated release resolves its media file and plays, instead of being
  refused with *no content files found in NZB*.
- PAR2 recovery volumes are recognised by content and never offered as playable
  media, even when they carry no `.par2` extension.
- Ordinary releases are unaffected and cost nothing extra: classification is
  subject-first, and no file is read unless the subjects fail to name it.

## When a release still fails

Name recovery cannot invent what is not there. A release stays unplayable when:

- Its articles have expired — obfuscation is irrelevant, the data is gone. See
  [Troubleshooting](troubleshooting.md).
- It is a fully obfuscated **RAR4** set missing its first volume, with no PAR2 to
  repair from. Nothing states which volume opens the set.
- It is packed in a format StreamNZB cannot stream (a compressed rather than
  STORE-mode archive), which is a limitation of the packing, not the naming.

Server logs at debug level show what each tier recovered — search for
`Recovered filenames for obfuscated release`.
