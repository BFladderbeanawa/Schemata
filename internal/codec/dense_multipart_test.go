package codec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func TestDensePartByteExactBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		filename string
		size     int
	}{
		{name: "empty", filename: "é.bin", size: 0},
		{name: "single zero", filename: "é.bin", size: 1},
		{name: "before chunk boundary", filename: "é.bin", size: 185},
		{name: "exact chunk boundary", filename: "é.bin", size: 186},
		{name: "after chunk boundary", filename: "é.bin", size: 187},
		{name: "manifest crosses chunk", filename: strings.Repeat("n", 250), size: 4097},
		{name: "filename crosses chunk", filename: strings.Repeat("n", 257), size: 513},
		{name: "persistent batch rollover", filename: "é.bin", size: 262145},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := make([]byte, tc.size)
			if tc.size > 2 {
				payload[1] = 255
			}
			part := singleDensePart(payload)
			v, err := EncodeDensePart(payload, tc.filename, DenseOptions{}, part)
			if err != nil {
				t.Fatal(err)
			}
			if len(v.Banners) != 0 {
				t.Fatal("default multipart volume contains banners")
			}
			for _, workers := range []int{1, 4} {
				file, err := Decode(context.Background(), v, workers)
				if err != nil {
					t.Fatal(err)
				}
				if file.Name != tc.filename || !bytes.Equal(file.Payload, payload) || file.Part == nil || *file.Part != part {
					t.Fatalf("workers %d: multipart recovery differs", workers)
				}
			}
		})
	}
}

func TestDensePartFixedManifestFields(t *testing.T) {
	t.Parallel()
	payload := []byte{0, 255, 0}
	part := PartInfo{
		TotalLength: 5,
		Offset:      0,
		Index:       0,
		Count:       2,
		Checksum:    0x12345678,
		PartSize:    3,
	}
	for i := range part.ID {
		part.ID[i] = byte(i)
	}
	manifest := marshalDensePart(part)
	want := make([]byte, 64)
	for i := range 32 {
		want[i] = byte(i)
	}
	want[39], want[55], want[63] = 5, 2, 3
	copy(want[56:60], []byte{0x12, 0x34, 0x56, 0x78})
	if !bytes.Equal(manifest[:], want) {
		t.Fatalf("manifest bytes = %x, want %x", manifest, want)
	}
	v, err := EncodeDensePart(payload, "x", DenseOptions{}, part)
	if err != nil {
		t.Fatal(err)
	}
	var header [prefixBytes]byte
	for i := range denseBootstrap {
		state := v.Palette[v.Blocks[i]]
		for nibble, color := range concreteNames {
			if state.Name == "minecraft:"+color+"_concrete" {
				header[i/2] |= byte(nibble) << uint(4*(1-i%2))
			}
		}
	}
	if header[8] != 4 || binary.BigEndian.Uint64(header[9:17]) != 3 || binary.BigEndian.Uint32(header[19:23]) != crc32.ChecksumIEEE(payload) {
		t.Fatalf("part-only header = %x", header)
	}
	file, err := Decode(context.Background(), v, 4)
	if err != nil || file.Part == nil || *file.Part != part || !bytes.Equal(file.Payload, payload) {
		t.Fatalf("fixed manifest decode: %v", err)
	}
	part.Index, part.Offset = 1, 3
	v, err = EncodeDensePart([]byte{173, 0}, "x", DenseOptions{}, part)
	if err != nil {
		t.Fatal(err)
	}
	file, err = Decode(context.Background(), v, 1)
	if err != nil || file.Part == nil || *file.Part != part || !bytes.Equal(file.Payload, []byte{173, 0}) {
		t.Fatalf("final short part decode: %v", err)
	}
}

func TestDensePartRejectsManifestInvariants(t *testing.T) {
	t.Parallel()
	payload := []byte{0, 255, 0}
	valid := PartInfo{TotalLength: 5, Count: 2, PartSize: 3}
	for _, tc := range []struct {
		name  string
		alter func(*PartInfo)
		want  string
	}{
		{name: "zero size", alter: func(p *PartInfo) { p.PartSize = 0 }, want: "part size"},
		{name: "oversize", alter: func(p *PartInfo) { p.PartSize = MaxDensePayload + 1 }, want: "part size"},
		{name: "zero count", alter: func(p *PartInfo) { p.Count = 0 }, want: "part count"},
		{name: "too many parts", alter: func(p *PartInfo) { p.Count = MaxDenseParts + 1 }, want: "part count"},
		{name: "wrong count", alter: func(p *PartInfo) { p.Count = 3 }, want: "part count"},
		{name: "out of range index", alter: func(p *PartInfo) { p.Index = 2 }, want: "part index"},
		{name: "wrong offset", alter: func(p *PartInfo) { p.Offset = 1 }, want: "part offset"},
		{name: "overflow offset", alter: func(p *PartInfo) { p.Offset = math.MaxUint64 }, want: "part offset"},
		{name: "overflow total", alter: func(p *PartInfo) { p.TotalLength = math.MaxUint64 }, want: "total length"},
		{name: "short final length", alter: func(p *PartInfo) { p.Index = 1; p.Offset = 3 }, want: "payload length"},
		{name: "empty file nonempty part", alter: func(p *PartInfo) { p.TotalLength = 0; p.Count = 1 }, want: "payload length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := valid
			tc.alter(&part)
			if _, err := EncodeDensePart(payload, "x", DenseOptions{}, part); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("encoder error = %v, want %q", err, tc.want)
			}
			// Construct valid symbol framing around an independently malformed manifest.
			manifest := marshalDensePart(part)
			v, err := encodeDense(context.Background(), payload, "x", DenseOptions{}, manifest[:], densePartVersion)
			if err != nil {
				t.Fatal(err)
			}
			for _, workers := range []int{1, 4} {
				if _, err := Decode(context.Background(), v, workers); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("workers %d: consumer error = %v, want %q", workers, err, tc.want)
				}
			}
		})
	}
}

func TestDensePartRejectsCorruption(t *testing.T) {
	t.Parallel()
	payload := []byte{0, 255, 0}
	for _, tc := range []struct {
		name  string
		alter func(*volume.Volume)
		want  string
	}{
		{name: "part crc", alter: func(v *volume.Volume) { denseSetHeaderByte(t, v, 19, 255) }, want: "payload checksum"},
		{name: "unsupported version", alter: func(v *volume.Volume) { denseSetHeaderByte(t, v, 8, 255) }, want: "unsupported encoding version"},
		{name: "untrusted length", alter: func(v *volume.Volume) {
			for i := 9; i < 17; i++ {
				denseSetHeaderByte(t, v, i, 255)
			}
		}, want: "32 MiB"},
		{name: "padding", alter: func(v *volume.Volume) {
			manifest := marshalDensePart(singleDensePart(payload))
			_, used, err := planDenseChunks(payload, "x", manifest[:], denseGeometry(), ordinaryFallback(), false)
			if err != nil {
				t.Fatal(err)
			}
			if used >= len(v.Blocks) {
				t.Fatal("fixture has no padding")
			}
			v.Blocks[used] = v.Blocks[0]
		}, want: "padding"},
		{name: "marker", alter: func(v *volume.Volume) {
			v.Palette = append(v.Palette, alphabet.States()[2])
			v.Blocks[denseBootstrap] = uint32(len(v.Palette) - 1)
		}, want: "marker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := EncodeDensePart(payload, "x", DenseOptions{}, singleDensePart(payload))
			if err != nil {
				t.Fatal(err)
			}
			tc.alter(&v)
			for _, workers := range []int{1, 4} {
				if _, err := Decode(context.Background(), v, workers); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("workers %d: error = %v, want %q", workers, err, tc.want)
				}
			}
		})
	}
}

func TestDensePartRejectsSinglePartFullHashCorruption(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		alter func(*PartInfo)
		want  string
	}{
		{name: "crc", alter: func(p *PartInfo) { p.Checksum ^= 1 }, want: "full-file checksum"},
		{name: "sha256", alter: func(p *PartInfo) { p.ID[0] ^= 1 }, want: "SHA256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte{0, 255, 0}
			part := singleDensePart(payload)
			tc.alter(&part)
			if _, err := EncodeDensePart(payload, "x", DenseOptions{}, part); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("encoder error: %v", err)
			}
			manifest := marshalDensePart(part)
			v, err := encodeDense(context.Background(), payload, "x", DenseOptions{}, manifest[:], densePartVersion)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(context.Background(), v, 4); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("consumer error: %v", err)
			}
		})
	}
}

func TestDensePartMaximumCanonicalCount(t *testing.T) {
	t.Parallel()
	part := PartInfo{
		TotalLength: uint64(MaxDensePayload) * MaxDenseParts,
		Offset:      uint64(MaxDensePayload) * (MaxDenseParts - 1),
		Index:       MaxDenseParts - 1,
		Count:       MaxDenseParts,
		PartSize:    MaxDensePayload,
	}
	if err := validateDensePart(part, MaxDensePayload); err != nil {
		t.Fatal(err)
	}
	manifest := marshalDensePart(part)
	if got := unmarshalDensePart(manifest[:]); got != part {
		t.Fatalf("maximum fields differ: %+v", got)
	}
}

func singleDensePart(payload []byte) PartInfo {
	return PartInfo{
		ID:          sha256.Sum256(payload),
		TotalLength: uint64(len(payload)),
		Count:       1,
		Checksum:    crc32.ChecksumIEEE(payload),
		PartSize:    uint32(max(1, len(payload))),
	}
}

func TestDensePartContextCancellation(t *testing.T) {
	t.Parallel()
	payload := []byte{0, 255, 0}
	part := singleDensePart(payload)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EncodeDensePartContext(ctx, payload, "x", DenseOptions{}, part); !errors.Is(err, context.Canceled) {
		t.Fatalf("encode cancellation: %v", err)
	}
	if _, err := EncodeDensePartContext(nil, payload, "x", DenseOptions{}, part); err == nil {
		t.Fatal("nil encode context accepted")
	}
}

func TestDensePartAllowsTotalBeyondVolumeLimit(t *testing.T) {
	t.Parallel()
	part := PartInfo{
		TotalLength: MaxDensePayload + 1,
		Offset:      MaxDensePayload,
		Index:       1024,
		Count:       1025,
		PartSize:    32768,
	}
	payload := []byte{0}
	v, err := EncodeDensePart(payload, "large.zip", DenseOptions{}, part)
	if err != nil {
		t.Fatal(err)
	}
	file, err := Decode(context.Background(), v, 4)
	if err != nil || file.Part == nil || *file.Part != part || !bytes.Equal(file.Payload, payload) {
		t.Fatalf("large full-file length round-trip: %v", err)
	}
}
