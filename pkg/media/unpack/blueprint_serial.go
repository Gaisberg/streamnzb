package unpack

import "encoding/json"

// serialBlueprintKindRAR marks a serialized plaintext RAR ArchiveBlueprint.
const serialBlueprintKindRAR = "rar"

// serializableArchiveBlueprint is a JSON-round-trippable form of an
// *ArchiveBlueprint. The live *ArchiveBlueprint holds UnpackableFile handles
// (with usenet fetchers) that cannot be serialized, so we persist each part's
// volume by NAME and re-link it to a freshly-built file on replay.
//
// Only plaintext STORE-mode archives are supported: compressed archives can't be
// streamed anyway, and encrypted ones would require persisting derived AES keys —
// those simply re-scan on replay instead.
type serializableArchiveBlueprint struct {
	Kind         string                       `json:"kind"`
	MainFileName string                       `json:"main_file_name"`
	TotalSize    int64                        `json:"total_size"`
	Target       EpisodeTarget                `json:"target"`
	Parts        []serializableVirtualPartDef `json:"parts"`
}

type serializableVirtualPartDef struct {
	VirtualStart int64  `json:"virtual_start"`
	VirtualEnd   int64  `json:"virtual_end"`
	VolFileName  string `json:"vol_file_name"`
	VolOffset    int64  `json:"vol_offset"`
}

// SerializeArchiveBlueprint converts a blueprint into a JSON form that can be
// rehydrated later, so a library replay can skip ScanArchive. It returns
// (nil, false) for anything that is not a reusable plaintext STORE-mode
// *ArchiveBlueprint (the caller should fall back to storing whatever it likes,
// or nothing).
func SerializeArchiveBlueprint(bp interface{}) ([]byte, bool) {
	ab, ok := bp.(*ArchiveBlueprint)
	if !ok || ab == nil {
		return nil, false
	}
	if ab.IsCompressed || ab.AnyEncrypted || len(ab.Parts) == 0 {
		return nil, false
	}
	s := serializableArchiveBlueprint{
		Kind:         serialBlueprintKindRAR,
		MainFileName: ab.MainFileName,
		TotalSize:    ab.TotalSize,
		Target:       ab.Target,
		Parts:        make([]serializableVirtualPartDef, 0, len(ab.Parts)),
	}
	for _, p := range ab.Parts {
		if p.VolFile == nil || p.VolFile.Name() == "" {
			return nil, false // can't re-link an unnamed volume
		}
		s.Parts = append(s.Parts, serializableVirtualPartDef{
			VirtualStart: p.VirtualStart,
			VirtualEnd:   p.VirtualEnd,
			VolFileName:  p.VolFile.Name(),
			VolOffset:    p.VolOffset,
		})
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, false
	}
	return data, true
}

// RehydrateArchiveBlueprint rebuilds a live *ArchiveBlueprint from JSON produced
// by SerializeArchiveBlueprint, re-linking each part's volume to a file in files
// by Name(). It is strictly best-effort: it returns (nil, false) if the JSON is
// not a serialized RAR blueprint or if any part's volume cannot be matched, so
// the caller safely falls back to a full ScanArchive.
func RehydrateArchiveBlueprint(data []byte, files []UnpackableFile) (*ArchiveBlueprint, bool) {
	if len(data) == 0 || len(files) == 0 {
		return nil, false
	}
	var s serializableArchiveBlueprint
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false
	}
	if s.Kind != serialBlueprintKindRAR || len(s.Parts) == 0 {
		return nil, false
	}

	byName := make(map[string]UnpackableFile, len(files))
	for _, f := range files {
		if f != nil {
			byName[f.Name()] = f
		}
	}

	bp := &ArchiveBlueprint{
		MainFileName: s.MainFileName,
		TotalSize:    s.TotalSize,
		Target:       s.Target,
		Parts:        make([]VirtualPartDef, 0, len(s.Parts)),
	}
	for _, sp := range s.Parts {
		vol, ok := byName[sp.VolFileName]
		if !ok {
			return nil, false // volume set no longer matches; re-scan instead
		}
		bp.Parts = append(bp.Parts, VirtualPartDef{
			VirtualStart: sp.VirtualStart,
			VirtualEnd:   sp.VirtualEnd,
			VolFile:      vol,
			VolOffset:    sp.VolOffset,
		})
	}
	return bp, true
}
