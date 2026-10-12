package codec_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func TestConcretePartByteExactBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		filename string
		size     int
	}{
		{name: "empty", filename: "名.bin"},
		{name: "zero", filename: "名.bin", size: 1},
		{name: "zero boundaries", filename: "名.bin", size: 4097},
		{name: "exact unit", filename: "x", size: 2048 - 23 - 1 - 64},
		{name: "manifest crosses unit", filename: strings.Repeat("n", 2048-23-32), size: 4099},
		{name: "UTF8 crosses unit", filename: strings.Repeat("n", 2048-24) + "名.bin", size: 4097},
		{name: "maximum filename", filename: strings.Repeat("名", 21845), size: 513},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := make([]byte, tc.size)
			if len(payload) > 2 {
				payload[1] = 255
			}
			part := singleConcretePart(payload)
			for _, policy := range []string{"balanced", "compact", "bounded"} {
				t.Run(policy, func(t *testing.T) {
					v, err := codec.EncodeConcretePartContext(
						context.Background(), payload, tc.filename, layout.Options{Policy: policy}, part,
					)
					if err != nil {
						t.Fatal(err)
					}
					assertColors(t, v, referenceConcretePartStream(tc.filename, payload, part))
					for _, workers := range []int{1, 4} {
						file, err := codec.Decode(context.Background(), v, workers)
						if err != nil {
							t.Fatal(err)
						}
						if file.Name != tc.filename || !bytes.Equal(file.Payload, payload) || file.Part == nil || *file.Part != part {
							t.Fatalf("workers %d: exact concrete part changed", workers)
						}
					}
				})
			}
		})
	}
}

func TestConcretePartCanonicalFinalAndMaximumCount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload []byte
		part    codec.PartInfo
	}{
		{name: "short final", payload: []byte{173, 0}, part: codec.PartInfo{
			TotalLength: 5, Offset: 3, Index: 1, Count: 2, PartSize: 3,
		}},
		{name: "4096 parts", payload: []byte{0}, part: codec.PartInfo{
			TotalLength: 4096, Offset: 4095, Index: 4095, Count: 4096, PartSize: 1,
		}},
		{name: "maximum effective size short final", payload: []byte{0}, part: codec.PartInfo{
			TotalLength: uint64(codec.ConcretePayloadCapacity("x", true)) + 1,
			Offset:      uint64(codec.ConcretePayloadCapacity("x", true)), Index: 1, Count: 2,
			PartSize: uint32(codec.ConcretePayloadCapacity("x", true)),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := codec.EncodeConcretePartContext(
				context.Background(), tc.payload, "x", layout.Options{}, tc.part,
			)
			if err != nil {
				t.Fatal(err)
			}
			assertColors(t, v, referenceConcretePartStream("x", tc.payload, tc.part))
			file, err := codec.Decode(context.Background(), v, 4)
			if err != nil || file.Part == nil || *file.Part != tc.part || !bytes.Equal(file.Payload, tc.payload) {
				t.Fatalf("canonical recovery: %v", err)
			}
		})
	}
}

func TestConcretePartRejectsManifestInvariants(t *testing.T) {
	t.Parallel()
	payload := []byte{0, 255, 0}
	valid := codec.PartInfo{TotalLength: 5, Count: 2, PartSize: 3}
	for _, tc := range []struct {
		name  string
		alter func(*codec.PartInfo)
		want  string
	}{
		{name: "zero size", alter: func(p *codec.PartInfo) { p.PartSize = 0 }, want: "part size"},
		{name: "over stream size", alter: func(p *codec.PartInfo) { p.PartSize = 32*1024*1024 + 1 }, want: "part size"},
		{name: "zero count", alter: func(p *codec.PartInfo) { p.Count = 0 }, want: "part count"},
		{name: "too many parts", alter: func(p *codec.PartInfo) { p.Count = 4097 }, want: "part count"},
		{name: "wrong count", alter: func(p *codec.PartInfo) { p.Count = 3 }, want: "part count"},
		{name: "index", alter: func(p *codec.PartInfo) { p.Index = 2 }, want: "part index"},
		{name: "offset", alter: func(p *codec.PartInfo) { p.Offset = 1 }, want: "part offset"},
		{name: "offset overflow", alter: func(p *codec.PartInfo) { p.Offset = math.MaxUint64 }, want: "part offset"},
		{name: "length overflow", alter: func(p *codec.PartInfo) { p.TotalLength = math.MaxUint64 }, want: "total length"},
		{name: "wrong final length", alter: func(p *codec.PartInfo) { p.Index = 1; p.Offset = 3 }, want: "payload length"},
		{name: "empty with payload", alter: func(p *codec.PartInfo) { p.TotalLength = 0; p.Count = 1 }, want: "payload length"},
		{name: "size does not reserve header", alter: func(p *codec.PartInfo) {
			p.PartSize = uint32(codec.ConcretePayloadCapacity("x", true) + 1)
			p.TotalLength = uint64(p.PartSize) + uint64(len(payload))
			p.Index, p.Offset = 1, uint64(p.PartSize)
		}, want: "concrete payload capacity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := valid
			tc.alter(&part)
			v, err := codec.EncodeConcretePartContext(
				context.Background(), payload, "x", layout.Options{}, part,
			)
			if err == nil || !strings.Contains(err.Error(), tc.want) || v.Colors != nil {
				t.Fatalf("encoder = %v, want %q without allocation", err, tc.want)
			}
			v = referenceVolume(16, 16, 16, referenceConcretePartStream("x", payload, part))
			for _, workers := range []int{1, 4} {
				file, err := codec.Decode(context.Background(), v, workers)
				if err == nil || !strings.Contains(err.Error(), tc.want) || file.Payload != nil || file.Part != nil {
					t.Fatalf("workers %d: consumer = %v, want %q", workers, err, tc.want)
				}
			}
		})
	}
}

func TestConcretePartRejectsCorruption(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
		want   string
	}{
		{name: "part CRC", mutate: func(s []byte) { s[19] ^= 1 }, want: "payload checksum"},
		{name: "payload", mutate: func(s []byte) { s[88] ^= 1 }, want: "payload checksum"},
		{name: "full CRC", mutate: func(s []byte) { s[24+56] ^= 1 }, want: "full-file checksum"},
		{name: "full SHA256", mutate: func(s []byte) { s[24] ^= 1 }, want: "SHA256"},
		{name: "padding", mutate: func(s []byte) { s[91] = 1 }, want: "padding"},
		{name: "last padding", mutate: func(s []byte) { s[len(s)-1] = 1 }, want: "padding"},
		{name: "unknown version", mutate: func(s []byte) { s[8] = 255 }, want: "unsupported encoding version"},
		{name: "dense version", mutate: func(s []byte) { s[8] = 4 }, want: "unsupported encoding version"},
		{name: "length overflow", mutate: func(s []byte) {
			binary.BigEndian.PutUint64(s[9:17], math.MaxUint64)
		}, want: "payload length"},
		{name: "canonical payload exceeds volume", mutate: func(s []byte) {
			binary.BigEndian.PutUint64(s[9:17], 2048)
			binary.BigEndian.PutUint64(s[24+32:24+40], 2048)
			binary.BigEndian.PutUint32(s[24+60:24+64], 2048)
		}, want: "payload length exceeds stream capacity"},
		{name: "manifest exceeds stream", mutate: func(s []byte) {
			binary.BigEndian.PutUint16(s[17:19], 2020)
			copy(s[23:2043], strings.Repeat("n", 2020))
		}, want: "manifest exceeds stream"},
		{name: "name exceeds stream", mutate: func(s []byte) {
			binary.BigEndian.PutUint16(s[17:19], 65535)
		}, want: "filename length"},
		{name: "invalid filename", mutate: func(s []byte) { s[23] = '/' }, want: "filename"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte{0, 255, 0}
			stream := make([]byte, 2048)
			copy(stream, referenceConcretePartStream("x", payload, singleConcretePart(payload)))
			tc.mutate(stream)
			v := referenceVolume(16, 16, 16, stream)
			for _, workers := range []int{1, 4} {
				file, err := codec.Decode(context.Background(), v, workers)
				if err == nil || !strings.Contains(err.Error(), tc.want) || file.Payload != nil || file.Part != nil {
					t.Fatalf("workers %d: error = %v, want %q", workers, err, tc.want)
				}
			}
		})
	}
}

func TestConcretePartEncoderLimitsAndCancellation(t *testing.T) {
	t.Parallel()
	payload := []byte{0, 255, 0}
	valid := singleConcretePart(payload)
	for _, name := range []string{"", "../x", "a\x00b", "\xff", strings.Repeat("n", 65536)} {
		if v, err := codec.EncodeConcretePartContext(
			context.Background(), payload, name, layout.Options{}, valid,
		); err == nil || v.Colors != nil {
			t.Fatalf("invalid filename %q accepted", name)
		}
	}
	for _, field := range []string{"crc", "sha"} {
		t.Run(field, func(t *testing.T) {
			part := valid
			if field == "crc" {
				part.Checksum ^= 1
			} else {
				part.ID[0] ^= 1
			}
			if v, err := codec.EncodeConcretePartContext(
				context.Background(), payload, "x", layout.Options{}, part,
			); err == nil || v.Colors != nil {
				t.Fatal("invalid aggregate checksum accepted")
			}
		})
	}
	oversize := make([]byte, codec.ConcretePayloadCapacity("x", true)+1)
	part := codec.PartInfo{TotalLength: uint64(len(oversize)), Count: 1, PartSize: uint32(len(oversize))}
	if v, err := codec.EncodeConcretePartContext(
		context.Background(), oversize, "x", layout.Options{}, part,
	); err == nil || v.Colors != nil {
		t.Fatal("oversized part accepted")
	}
	if v, err := codec.EncodeConcretePartContext(
		context.Background(), payload, "x", layout.Options{MaxHeight: 400}, valid,
	); err == nil || v.Colors != nil {
		t.Fatal("invalid layout accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := codec.EncodeConcretePartContext(ctx, payload, "x", layout.Options{}, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := codec.EncodeConcretePartContext(nil, payload, "x", layout.Options{}, valid); err == nil {
		t.Fatal("nil context accepted")
	}
	for _, multipart := range []bool{false, true} {
		t.Run(fmt.Sprint(multipart), func(t *testing.T) {
			want := volume.MaxBlocks/2 - 23 - len("名.bin")
			if multipart {
				want -= 64
			}
			if got := codec.ConcretePayloadCapacity("名.bin", multipart); got != want {
				t.Fatalf("capacity = %d, want %d", got, want)
			}
		})
	}
}

func TestConcretePartCancellationDuringEncoding(t *testing.T) {
	t.Parallel()
	payload := make([]byte, 64*1024)
	part := codec.PartInfo{TotalLength: 2 * uint64(len(payload)), Count: 2, PartSize: uint32(len(payload))}
	ctx := &concreteCancelAfterContext{Context: context.Background(), remaining: 8}
	v, err := codec.EncodeConcretePartContext(ctx, payload, "x", layout.Options{}, part)
	if !errors.Is(err, context.Canceled) || v.Colors != nil {
		t.Fatalf("bounded encoding cancellation = %v", err)
	}
}

type concreteCancelAfterContext struct {
	context.Context
	remaining int
}

func (ctx *concreteCancelAfterContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func singleConcretePart(payload []byte) codec.PartInfo {
	return codec.PartInfo{
		ID: sha256.Sum256(payload), TotalLength: uint64(len(payload)), Count: 1,
		Checksum: crc32.ChecksumIEEE(payload), PartSize: uint32(max(1, len(payload))),
	}
}

func referenceConcretePartStream(name string, payload []byte, part codec.PartInfo) []byte {
	stream := make([]byte, 23+len(name)+64+len(payload))
	copy(stream, "SCHEMATA")
	stream[8] = 5
	binary.BigEndian.PutUint64(stream[9:17], uint64(len(payload)))
	binary.BigEndian.PutUint16(stream[17:19], uint16(len(name)))
	binary.BigEndian.PutUint32(stream[19:23], crc32.ChecksumIEEE(payload))
	copy(stream[23:], name)
	manifest := stream[23+len(name):]
	copy(manifest[:32], part.ID[:])
	binary.BigEndian.PutUint64(manifest[32:40], part.TotalLength)
	binary.BigEndian.PutUint64(manifest[40:48], part.Offset)
	binary.BigEndian.PutUint32(manifest[48:52], part.Index)
	binary.BigEndian.PutUint32(manifest[52:56], part.Count)
	binary.BigEndian.PutUint32(manifest[56:60], part.Checksum)
	binary.BigEndian.PutUint32(manifest[60:64], part.PartSize)
	copy(manifest[64:], payload)
	return stream
}
