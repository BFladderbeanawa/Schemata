//go:build integration

package schematic_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
)

func TestConcreteMultipartBothAdaptersByteExact(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		filename string
		payload  []byte
	}{
		{name: "empty", filename: "空.bin", payload: []byte{}},
		{name: "zero", filename: "零.bin", payload: []byte{0}},
		{name: "leading trailing zeros", filename: "résumé.bin", payload: []byte{0, 255, 17, 0, 0}},
		{name: "manifest crosses unit", filename: strings.Repeat("n", 2048-23-32),
			payload: bytes.Repeat([]byte{0, 255, 173, 0}, 2049)},
		{name: "unicode crosses unit", filename: strings.Repeat("n", 2048-24) + "名.bin",
			payload: bytes.Repeat([]byte{0, 17, 255, 0}, 3073)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, ext := range []string{".litematic", ".schematic"} {
				t.Run(ext, func(t *testing.T) {
					const partSize = 4097
					count := max(1, (len(tc.payload)+partSize-1)/partSize)
					for index := range count {
						start := index * partSize
						payload := tc.payload[start:min(len(tc.payload), start+partSize)]
						part := codec.PartInfo{
							ID: sha256.Sum256(tc.payload), TotalLength: uint64(len(tc.payload)),
							Offset: uint64(start), Index: uint32(index), Count: uint32(count),
							Checksum: crc32.ChecksumIEEE(tc.payload), PartSize: partSize,
						}
						v, err := codec.EncodeConcretePartContext(
							context.Background(), payload, tc.filename, layout.Options{}, part,
						)
						if err != nil {
							t.Fatal(err)
						}
						var encoded bytes.Buffer
						if err := schematic.Write(&encoded, ext, v); err != nil {
							t.Fatal(err)
						}
						parsed, err := schematic.Read(bytes.NewReader(encoded.Bytes()), ext)
						if err != nil {
							t.Fatal(err)
						}
						if parsed.Blocks != nil || parsed.X != v.X || parsed.Y != v.Y || parsed.Z != v.Z ||
							!bytes.Equal(parsed.Colors, v.Colors) {
							t.Fatal("concrete unit blocks changed across adapter")
						}
						for _, workers := range []int{1, 4} {
							file, err := codec.Decode(context.Background(), parsed, workers)
							if err != nil {
								t.Fatalf("part %d workers %d: %v", index, workers, err)
							}
							if file.Name != tc.filename || !bytes.Equal(file.Payload, payload) || file.Part == nil || *file.Part != part {
								t.Fatalf("part %d workers %d: serialized recovery differs", index, workers)
							}
						}
						// The in-block manifest is adapter-independent, not metadata.
						otherExt := ".schematic"
						if ext == otherExt {
							otherExt = ".litematic"
						}
						encoded.Reset()
						if err := schematic.Write(&encoded, otherExt, parsed); err != nil {
							t.Fatal(err)
						}
						converted, err := schematic.Read(bytes.NewReader(encoded.Bytes()), otherExt)
						if err != nil {
							t.Fatal(err)
						}
						file, err := codec.Decode(context.Background(), converted, 4)
						if err != nil || file.Part == nil || *file.Part != part || !bytes.Equal(file.Payload, payload) {
							t.Fatalf("adapter conversion lost identity: %v", err)
						}
					}
				})
			}
		})
	}
}

func TestConcreteVersionsAndCorruptionThroughAdapters(t *testing.T) {
	t.Parallel()
	payload := []byte{0, 255, 173, 0}
	part := codec.PartInfo{
		ID: sha256.Sum256(payload), TotalLength: uint64(len(payload)), Count: 1,
		Checksum: crc32.ChecksumIEEE(payload), PartSize: uint32(len(payload)),
	}
	for _, ext := range []string{".litematic", ".schematic"} {
		for _, version := range []byte{1, 5, 255} {
			t.Run(ext+fmt.Sprint(version), func(t *testing.T) {
				v, err := codec.EncodeConcretePartContext(
					context.Background(), payload, "original.bin", layout.Options{}, part,
				)
				if version == 1 {
					v, err = codec.Encode(payload, "original.bin", layout.Options{})
				}
				if err != nil {
					t.Fatal(err)
				}
				if version == 255 {
					// Version nibbles are x=1,z=0/1 in the first concrete unit.
					v.Colors[v.Index(1, 0, 0)] = 15
					v.Colors[v.Index(1, 0, 1)] = 15
				}
				var encoded bytes.Buffer
				if err := schematic.Write(&encoded, ext, v); err != nil {
					t.Fatal(err)
				}
				parsed, err := schematic.Read(bytes.NewReader(encoded.Bytes()), ext)
				if err != nil {
					t.Fatal(err)
				}
				file, err := codec.Decode(context.Background(), parsed, 4)
				if version == 255 {
					if err == nil || !strings.Contains(err.Error(), "unsupported encoding version") {
						t.Fatalf("unknown unit version accepted: %v", err)
					}
					return
				}
				if err != nil || file.Name != "original.bin" || !bytes.Equal(file.Payload, payload) {
					t.Fatalf("version %d recovery differs: %v", version, err)
				}
				if version == 1 && file.Part != nil {
					t.Fatal("version 1 unexpectedly became multipart")
				}
				if version == 5 && (file.Part == nil || *file.Part != part) {
					t.Fatal("version 5 manifest changed")
				}
				// Corrupt a selected white padding block after adapter parsing.
				parsed.Colors[len(parsed.Colors)-1] = 1
				if _, err := codec.Decode(context.Background(), parsed, 4); err == nil || !strings.Contains(err.Error(), "padding") {
					t.Fatalf("adapter padding corruption accepted: %v", err)
				}
			})
		}
	}
}
