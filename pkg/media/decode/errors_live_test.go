package decode

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"testing"
)

// The canary for the one thing this package cannot get by asking: how the
// decoder words itself.
//
// classify and IsCorrupt match rapidyenc's error text, because rapidyenc
// reports everything as a formatted string and we do not own it. The tests in
// errors_test.go pin that matching against strings written out by hand, which
// proves the matcher works and proves nothing at all about whether those are
// still the strings rapidyenc produces — an upstream rewording would leave
// them green while corruption silently stopped being recognised in production,
// and a release that should have failed over would play on as damaged.
//
// So these damage real articles, run them through the real decoder, and assert
// the classification against whatever error actually comes back. When a
// dependency bump changes the wording, this fails, and corruptPhrases is the
// one place to fix.

// yencArticle is a valid single-part article on the wire, built with the same
// encoder the round-trip tests use.
func yencArticle(t *testing.T, payload []byte) []byte {
	t.Helper()
	return buildWire(t, payload, false)
}

func TestLiveDecoderCorruptionIsRecognised(t *testing.T) {
	payload := bytes.Repeat([]byte("streamnzb yenc canary payload "), 64)

	for _, tc := range []struct {
		name   string
		damage func([]byte) []byte
	}{
		{
			// Cut off mid-payload, so the article ends before its trailer.
			// That is what a truncated or partially-delivered post looks
			// like, and it is the case the "=yend" phrases exist for.
			name: "truncated mid payload",
			damage: func(wire []byte) []byte {
				cut := len(wire) * 6 / 10
				return append(append([]byte{}, wire[:cut]...), []byte("\r\n.\r\n")...)
			},
		},
		{
			// A wrong CRC in the trailer is the decoder's own integrity check
			// failing, which is the definition of damaged bytes.
			name: "trailer crc broken",
			damage: func(wire []byte) []byte {
				re := regexp.MustCompile(`crc32=[0-9a-fA-F]+`)
				if !re.Match(wire) {
					t.Skip("encoder emitted no crc32 to break")
				}
				return re.ReplaceAll(wire, []byte("crc32=deadbeef"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.damage(yencArticle(t, payload))
			_, err := DecodeToBytes(bytes.NewReader(wire))
			if err == nil {
				t.Skip("this damage decoded cleanly — nothing to classify")
			}
			t.Logf("decoder said: %v", err)
			if !IsCorrupt(err) {
				t.Errorf("the decoder reported damage that IsCorrupt does not recognise.\n"+
					"  error: %v\n"+
					"  fix: add its wording to corruptPhrases in errors.go", err)
			}
		})
	}
}

// The size error carries the numbers the tolerance rule reads, so the format
// it is written in is load-bearing. A reword here does not merely stop
// corruption being recognised — it makes every short final segment fail, which
// is the common case rather than the rare one.
func TestLiveDecoderSizeMismatchIsParsed(t *testing.T) {
	payload := bytes.Repeat([]byte("size canary "), 64)
	wire := yencArticle(t, payload)

	// Overstate the declared size so the decode completes but comes up short,
	// exactly as a poster's rounded "=yend size=" does.
	re := regexp.MustCompile(`(=yend[^\r\n]*size=)(\d+)`)
	m := re.FindSubmatch(wire)
	if m == nil {
		t.Skip("encoder emitted no =yend size= to overstate")
	}
	declared, err := strconv.Atoi(string(m[2]))
	if err != nil {
		t.Fatalf("parsing the fixture's declared size: %v", err)
	}
	overstated := declared + int(maxDecodeSizeTolerance) + 1000
	wire = re.ReplaceAll(wire, []byte("${1}"+strconv.Itoa(overstated)))

	_, err = DecodeToBytes(bytes.NewReader(wire))
	if err == nil {
		t.Fatal("overstating the size well past the tolerance should have failed the decode")
	}

	var mismatch *SizeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("the decoder reported a size failure that classify did not parse.\n"+
			"  error: %v\n"+
			"  fix: update sizeMismatchRE in errors.go", err)
	}
	if mismatch.Expected != int64(overstated) {
		t.Errorf("parsed expected size %d, want %d", mismatch.Expected, overstated)
	}
	if mismatch.Shortfall() <= 0 {
		t.Errorf("Shortfall() = %d, want a positive shortfall", mismatch.Shortfall())
	}
}

// Rounded sizes are tolerated, but must not hide a failed CRC check. Otherwise
// damaged segments are cached as successful downloads and played as corrupt data.
func TestRoundedSizeStillChecksCRC(t *testing.T) {
	payload := bytes.Repeat([]byte("size and crc canary "), 64)
	for _, multipart := range []bool{false, true} {
		for _, shortfall := range []int{1, maxDecodeSizeTolerance} {
			for _, chunk := range []int{1, 127, 16 << 10, 32 << 10} {
				for _, badCRC := range []bool{false, true} {
					t.Run(fmt.Sprintf("multipart=%t/shortfall=%d/chunk=%d/badCRC=%t", multipart, shortfall, chunk, badCRC), func(t *testing.T) {
						wire := manualYencArticle(payload, 128)
						fileSize := len(payload)
						if !multipart {
							wire = bytes.Replace(wire, []byte(" part=1 total=1"), nil, 1)
							wire = regexp.MustCompile(`=ypart[^\r\n]*\r\n`).ReplaceAll(wire, nil)
							wire = bytes.Replace(wire, []byte("pcrc32="), []byte("crc32="), 1)
							fileSize += shortfall
							wire = bytes.Replace(wire, []byte(fmt.Sprintf(" size=%d name=", len(payload))), []byte(fmt.Sprintf(" size=%d name=", fileSize)), 1)
						}
						wire = bytes.Replace(wire, []byte(fmt.Sprintf("=yend size=%d ", len(payload))), []byte(fmt.Sprintf("=yend size=%d ", len(payload)+shortfall)), 1)
						if badCRC {
							wire = regexp.MustCompile(`crc32=[0-9a-fA-F]+`).ReplaceAll(wire, []byte("crc32=deadbeef"))
						}
						wire = stuffAndTerminate(wire, false)
						frame, err := DecodeToBytesSized(benchmarkWireReader{bytes.NewReader(wire), chunk}, 511)
						if badCRC {
							if err == nil || frame != nil {
								t.Fatal("rounded size hid invalid CRC")
							}
							if !IsCorrupt(err) {
								t.Fatalf("CRC failure not classified as corruption: %v", err)
							}
						} else {
							if err != nil {
								t.Fatal(err)
							}
							if !bytes.Equal(frame.Data, payload) {
								t.Fatal("rounded size changed payload")
							}
							if frame.FileSize != int64(fileSize) || frame.PartOffset != 0 {
								t.Fatal("geometry changed")
							}
						}
					})
				}
			}
		}
	}
}
