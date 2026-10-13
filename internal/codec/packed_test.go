package codec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

// packedPart builds part index of payload exactly as the CLI does.
func packedPart(t *testing.T, payload []byte, name string, partSize uint32, index uint32) (PackedHeader, []byte) {
	t.Helper()
	count, err := PackedPartCount(uint64(len(payload)), partSize)
	if err != nil {
		t.Fatal(err)
	}
	digests := make([][32]byte, count)
	for i := range count {
		start := min(uint64(i)*uint64(partSize), uint64(len(payload)))
		end := min(start+uint64(partSize), uint64(len(payload)))
		digests[i] = sha256.Sum256(payload[start:end])
	}
	offset := uint64(index) * uint64(partSize)
	original := payload[offset:min(offset+uint64(partSize), uint64(len(payload)))]
	c, err := NewPackedCompressor()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	method, data := c.Compress(original)
	h := PackedHeader{
		Method: method, FullLength: uint64(len(payload)), Offset: offset, Index: index, Count: count,
		PartSize: partSize, DataLength: uint64(len(data)), Checksum: crc32.ChecksumIEEE(original),
		Digest: digests[index], Group: PackedGroup(name, uint64(len(payload)), partSize, count, digests[0]),
	}
	if h.Last() {
		h.Aggregate = PackedAggregate(digests)
	}
	return h, append([]byte(nil), data...)
}

func encodePackedPart(t *testing.T, payload []byte, name string, partSize, index uint32, opts layout.Options) volume.Volume {
	t.Helper()
	h, data := packedPart(t, payload, name, partSize, index)
	v, err := EncodePacked(context.Background(), name, h, data, opts)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPackedRoundTripMethodsAndLayouts(t *testing.T) {
	t.Parallel()
	random := make([]byte, 5000)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		payload []byte
		method  byte
	}{
		{name: "empty", payload: []byte{}, method: PackedStored},
		{name: "one zero byte", payload: []byte{0}, method: PackedStored},
		{name: "incompressible", payload: random, method: PackedStored},
		{name: "compressible with zero edges", payload: append(append([]byte{0, 0}, bytes.Repeat([]byte("schemata "), 3000)...), 0, 0), method: PackedZstd},
	} {
		for _, policy := range []string{"compact", "balanced", "bounded"} {
			t.Run(tc.name+"/"+policy, func(t *testing.T) {
				opts := layout.Options{Policy: policy, PaddingPercent: 25}
				v := encodePackedPart(t, tc.payload, "数据 file.bin", 1<<20, 0, opts)
				part, err := DecodePacked(context.Background(), v)
				if err != nil {
					t.Fatal(err)
				}
				if part.Name != "数据 file.bin" || !bytes.Equal(part.Data, tc.payload) || part.Header.Method != tc.method {
					t.Fatalf("recovery differs: method %d", part.Header.Method)
				}
				required, _ := PackedRequiredPositions(len(part.Name), part.Header.DataLength)
				if n := v.X * v.Y * v.Z; n < required || (policy == "compact" && n != required) {
					t.Fatalf("%d positions for %d required", n, required)
				}
			})
		}
	}
}

func TestPackedMultipartHeadersAndAggregate(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte{0, 1, 2, 3, 255}, 1000)
	const partSize = 2048
	var digests [][32]byte
	var group [16]byte
	for index := range uint32(3) {
		part, err := DecodePacked(context.Background(), encodePackedPart(t, payload, "a.bin", partSize, index, layout.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		h := part.Header
		if !bytes.Equal(part.Data, payload[h.Offset:h.Offset+h.PartLength()]) || h.Count != 3 {
			t.Fatalf("part %d differs", index)
		}
		if index == 0 {
			group = h.Group
		} else if h.Group != group {
			t.Fatal("group identifier differs between parts")
		}
		if (h.Aggregate != [32]byte{}) != h.Last() {
			t.Fatalf("aggregate placement wrong in part %d", index)
		}
		digests = append(digests, h.Digest)
		if h.Last() && PackedAggregate(digests) != h.Aggregate {
			t.Fatal("aggregate digest mismatch")
		}
	}
}

func TestPackedRejectsCorruption(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("abcdefgh"), 500)
	for _, tc := range []struct {
		name  string
		alter func(v *volume.Volume)
		want  string
	}{
		{name: "version", alter: func(v *volume.Volume) { v.Packed[streamIndex(8)] = 7 }, want: "unsupported packed encoding version 7"},
		{name: "header field", alter: func(v *volume.Volume) { v.Packed[streamIndex(30)] ^= 1 }, want: "header checksum"},
		{name: "basename", alter: func(v *volume.Volume) { v.Packed[streamIndex(136)] ^= 1 }, want: "header checksum"},
		{name: "data section", alter: func(v *volume.Volume) { v.Packed[streamIndex(150)] ^= 0x40 }, want: ""},
		{name: "padding", alter: func(v *volume.Volume) { v.Packed[len(v.Packed)-9] = 1 }, want: "nonzero packed padding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := encodePackedPart(t, payload, "a.bin", 1<<20, 0, layout.Options{Policy: "bounded", PaddingPercent: 100})
			tc.alter(&v)
			part, err := DecodePacked(context.Background(), v)
			if err == nil || !strings.Contains(err.Error(), tc.want) || part.Data != nil {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPackedRejectsNoncanonicalHeaders(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte{9}, 4096)
	base, data := packedPart(t, payload, "a.bin", 1<<20, 0)
	stored := base
	stored.Method, stored.DataLength = PackedStored, uint64(len(payload))
	for _, tc := range []struct {
		name  string
		alter func(h *PackedHeader)
		data  []byte
		want  string
	}{
		{name: "method", alter: func(h *PackedHeader) { h.Method = 2 }, data: data, want: "unknown packed data method"},
		{name: "count", alter: func(h *PackedHeader) { h.Count = 2 }, data: data, want: "not canonical"},
		{name: "offset", alter: func(h *PackedHeader) { h.Offset = 1 }, data: data, want: "offset"},
		{name: "part size", alter: func(h *PackedHeader) { h.PartSize = MaxPackedPartSize + 1 }, data: data, want: "outside"},
		{name: "zstd not shorter", alter: func(h *PackedHeader) {
			h.Method, h.DataLength = PackedZstd, uint64(len(payload))
		}, data: payload, want: "zstd packed data length"},
		{name: "stored length", alter: func(h *PackedHeader) { *h = stored; h.DataLength-- }, data: payload[1:], want: "stored packed data length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := base
			tc.alter(&h)
			if _, err := EncodePacked(context.Background(), "a.bin", h, tc.data, layout.Options{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := EncodePacked(context.Background(), "..", base, data, layout.Options{}); err == nil {
		t.Fatal("traversal basename accepted")
	}
}

// Corrupt frames are given valid outer CRC fields so only zstd checks fire.
func TestPackedRejectsInvalidZstdFrames(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("frame "), 2000)
	h, frame := packedPart(t, payload, "a.bin", 1<<20, 0)
	if h.Method != PackedZstd {
		t.Fatal("fixture did not compress")
	}
	for _, tc := range []struct {
		name  string
		frame []byte
		p     uint64
		want  string
	}{
		{name: "trailing", frame: append(append([]byte(nil), frame...), 0), p: h.PartLength(), want: ""},
		{name: "wrong content size", frame: frame, p: h.PartLength() - 1, want: ""},
		{name: "truncated", frame: frame[:len(frame)-1], p: h.PartLength(), want: ""},
		{name: "skippable", frame: []byte{0x50, 0x2a, 0x4d, 0x18, 0, 0, 0, 0}, p: 10, want: "skippable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := decompressPacked(tc.frame, tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("decoded %d bytes, error %v", len(out), err)
			}
		})
	}
	checksummed := append([]byte(nil), frame...)
	checksummed[4] |= 1 << 2 // Frame_Header_Descriptor content checksum flag.
	if _, err := decompressPacked(checksummed, h.PartLength()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum flag accepted: %v", err)
	}
	if out, err := decompressPacked(frame, h.PartLength()); err != nil || !bytes.Equal(out, payload) {
		t.Fatalf("valid frame rejected: %v", err)
	}
}

// Encoding is a pure function of bytes, name, and options; compression is
// single-threaded per part, so the logical structure is deterministic.
func TestPackedDeterministicStructure(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("deterministic"), 4000)
	a := encodePackedPart(t, payload, "a.bin", 1<<20, 0, layout.Options{})
	b := encodePackedPart(t, payload, "a.bin", 1<<20, 0, layout.Options{})
	if a.X != b.X || a.Y != b.Y || a.Z != b.Z || !bytes.Equal(a.Packed, b.Packed) {
		t.Fatal("packed structure differs between identical encodings")
	}
}

func TestPackedRequiredPositionsBoundary(t *testing.T) {
	t.Parallel()
	// 136 + 65535 + 82,837,504 bytes is the worst stored case; it must fit 64 Mi.
	if _, err := PackedRequiredPositions(65535, MaxPackedPartSize); err != nil {
		t.Fatal(err)
	}
	if _, err := PackedRequiredPositions(1, MaxPackedPartSize+1); err == nil {
		t.Fatal("oversized data section accepted")
	}
	if got, _ := PackedRequiredPositions(1, 3); got != (8*140+9)/10 {
		t.Fatalf("required positions = %d", got)
	}
}
