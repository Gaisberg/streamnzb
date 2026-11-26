package par2

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func packet(typ string, body []byte) []byte {
	var b bytes.Buffer
	b.Write(magic)
	total := uint64(headerLen + len(body))
	b.Write(binary.LittleEndian.AppendUint64(nil, total))
	b.Write(make([]byte, 16)) // packet md5, not checked
	b.Write(make([]byte, 16)) // set id
	t := make([]byte, 16)
	copy(t, typ)
	b.Write(t)
	b.Write(body)
	return b.Bytes()
}

func fileDesc(id byte, length uint64, name string) []byte {
	body := make([]byte, 0, 56+len(name)+4)
	fid := make([]byte, 16)
	fid[0] = id
	body = append(body, fid...)
	body = append(body, bytes.Repeat([]byte{0xAA}, 16)...)
	body = append(body, bytes.Repeat([]byte{0xBB}, 16)...)
	body = binary.LittleEndian.AppendUint64(body, length)
	body = append(body, []byte(name)...)
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	return packet("PAR 2.0\x00FileDesc", body)
}

func TestParseIndexReadsFileDescriptions(t *testing.T) {
	var data []byte
	data = append(data, []byte("junk before")...)
	data = append(data, packet("PAR 2.0\x00Main", make([]byte, 12))...)
	data = append(data, fileDesc(1, 1654558871, "House.of.the.Dragon.S03E01.1080p.WEB-DL-GRP.mkv")...)
	data = append(data, fileDesc(2, 1891533106, "House.of.the.Dragon.S03E02.1080p.WEB-DL-GRP.mkv")...)
	data = append(data, fileDesc(1, 1654558871, "House.of.the.Dragon.S03E01.1080p.WEB-DL-GRP.mkv")...) // repeated packet
	data = append(data, packet("PAR 2.0\x00Creator", []byte("test\x00\x00\x00\x00"))...)
	data = append(data, magic[:4]...) // truncated tail

	descs, err := ParseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(descs) != 2 {
		t.Fatalf("descs = %d", len(descs))
	}
	if descs[0].Name != "House.of.the.Dragon.S03E01.1080p.WEB-DL-GRP.mkv" || descs[0].Length != 1654558871 {
		t.Fatalf("first = %+v", descs[0])
	}
	if descs[0].MD5[0] != 0xAA || descs[0].MD5First16K[0] != 0xBB {
		t.Fatal("hashes misread")
	}
	byLen := NamesByLength(descs)
	if names := byLen[1891533106]; len(names) != 1 || names[0] != descs[1].Name {
		t.Fatalf("by length = %v", byLen)
	}
}

func TestParseIndexRefusesNonPar2(t *testing.T) {
	if _, err := ParseIndex([]byte("this is an nfo file, not a par2")); err == nil {
		t.Fatal("expected an error")
	}
	bad := packet("PAR 2.0\x00FileDesc", nil)
	binary.LittleEndian.PutUint64(bad[8:16], 1<<40)
	if _, err := ParseIndex(bad); err == nil {
		t.Fatal("absurd packet length must be an error")
	}
}
