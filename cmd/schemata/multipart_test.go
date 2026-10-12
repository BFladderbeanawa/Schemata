//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

var multipartFormats = []struct {
	name, encoding, extension string
}{
	{name: "dense litematic", encoding: "dense", extension: ".litematic"},
	{name: "concrete litematic", encoding: "concrete", extension: ".litematic"},
	{name: "concrete schematic", encoding: "concrete", extension: ".schematic"},
}

func TestMultipartCLIRecoveryAndCollisions(t *testing.T) {
	t.Parallel()
	for _, format := range multipartFormats {
		t.Run(format.name, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "数据.zip")
			payload := bytes.Repeat([]byte{0, 173, 255, 17, 0}, 420000)
			if err := os.WriteFile(input, payload, 0600); err != nil {
				t.Fatal(err)
			}
			requested := filepath.Join(dir, "archive"+format.extension)
			encodeArgs := []string{"encode", "--encoding", format.encoding, "--part-size-mib", "1", input, requested}
			execute(t, encodeArgs, 0)
			parts := filepath.Join(dir, "archive.parts")
			entries, err := os.ReadDir(parts)
			if err != nil || len(entries) != 3 {
				t.Fatalf("parts: %v, %v", entries, err)
			}
			// Part identity comes from structure fields, not filenames or listing order.
			original := filepath.Join(parts, "000001"+format.extension)
			renamed := filepath.Join(parts, "z"+format.extension)
			if err := os.Rename(original, renamed); err != nil {
				t.Fatal(err)
			}
			for _, workers := range []string{"1", "4"} {
				execute(t, []string{"decode", "--workers", workers, parts, filepath.Join(dir, "out")}, 0)
			}
			for _, name := range []string{"数据.zip", "数据 (1).zip"} {
				got, err := os.ReadFile(filepath.Join(dir, "out", name))
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("recovery %s: %v", name, err)
				}
			}
			execute(t, []string{
				"decode", "--filename", "replacement.bin", filepath.Join(parts, "000002"+format.extension),
				filepath.Join(dir, "replacement"),
			}, 0)
			got, err := os.ReadFile(filepath.Join(dir, "replacement", "replacement.bin"))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("part-file recovery: %v", err)
			}
			execute(t, encodeArgs, 0)
			if _, err := os.Stat(filepath.Join(dir, "archive (1).parts")); err != nil {
				t.Fatal(err)
			}
			// Reserving the second set must not alter the first set.
			execute(t, []string{"decode", parts, filepath.Join(dir, "unchanged")}, 0)
			got, err = os.ReadFile(filepath.Join(dir, "unchanged", "数据.zip"))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("existing set changed: %v", err)
			}
		})
	}
}

func TestMultipartCLIRejectsIncompleteAndMixedSets(t *testing.T) {
	t.Parallel()
	for _, format := range multipartFormats {
		t.Run(format.name, func(t *testing.T) {
			for _, failure := range []string{"missing", "duplicate", "mixed", "corrupt", "single volume"} {
				t.Run(failure, func(t *testing.T) {
					dir := t.TempDir()
					input := filepath.Join(dir, "data.bin")
					payload := []byte{0, 255, 19, 17}
					if err := os.WriteFile(input, payload, 0600); err != nil {
						t.Fatal(err)
					}
					opts := partsOptions{Encoding: format.encoding, Extension: format.extension, PartSize: 2}
					parts, err := encodeParts(context.Background(), input, filepath.Join(dir, "archive"+format.extension), opts)
					if err != nil {
						t.Fatal(err)
					}
					second := filepath.Join(parts, "000002"+format.extension)
					switch failure {
					case "missing":
						if err := os.Remove(second); err != nil {
							t.Fatal(err)
						}
					case "duplicate":
						data, err := os.ReadFile(filepath.Join(parts, "000001"+format.extension))
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(second, data, 0600); err != nil {
							t.Fatal(err)
						}
					case "mixed":
						payload[0] = 17
						if err := os.WriteFile(input, payload, 0600); err != nil {
							t.Fatal(err)
						}
						other, err := encodeParts(
							context.Background(),
							input,
							filepath.Join(dir, "other"+format.extension),
							opts,
						)
						if err != nil {
							t.Fatal(err)
						}
						data, err := os.ReadFile(filepath.Join(other, "000002"+format.extension))
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(second, data, 0600); err != nil {
							t.Fatal(err)
						}
					case "corrupt":
						if err := os.WriteFile(second, []byte("truncated"), 0600); err != nil {
							t.Fatal(err)
						}
					case "single volume":
						v, err := codec.Encode(payload, "data.bin", layout.Options{})
						if err != nil {
							t.Fatal(err)
						}
						writeStructure(t, second, func(w io.Writer) error { return schematic.Write(w, format.extension, v) })
					}
					dest := filepath.Join(dir, "out")
					execute(t, []string{"decode", parts, dest}, 1)
					if _, err := os.Stat(dest); !os.IsNotExist(err) {
						t.Fatalf("failed set published destination: %v", err)
					}
				})
			}
		})
	}
}

func TestMultipartCLIEmptyAndCancellation(t *testing.T) {
	t.Parallel()
	for _, format := range multipartFormats {
		t.Run(format.name, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "empty.bin")
			if err := os.WriteFile(input, []byte{}, 0600); err != nil {
				t.Fatal(err)
			}
			execute(t, []string{
				"encode", "--encoding", format.encoding, "--part-size-mib", "32", input,
				filepath.Join(dir, "empty"+format.extension),
			}, 0)
			parts := filepath.Join(dir, "empty.parts")
			v, err := readStructure(filepath.Join(parts, "000001"+format.extension), format.extension)
			if err != nil {
				t.Fatal(err)
			}
			file, err := codec.Decode(context.Background(), v, 1)
			if err != nil {
				t.Fatal(err)
			}
			wantSize := 32 * 1024 * 1024
			if format.encoding == "concrete" {
				wantSize -= 23 + len("empty.bin") + 64
			}
			if file.Part == nil || file.Part.PartSize != uint32(wantSize) || file.Part.Count != 1 {
				t.Fatalf("empty manifest = %+v, want one part of canonical size %d", file.Part, wantSize)
			}
			execute(t, []string{"decode", parts, filepath.Join(dir, "out")}, 0)
			got, err := os.ReadFile(filepath.Join(dir, "out", "empty.bin"))
			if err != nil || len(got) != 0 {
				t.Fatalf("empty recovery: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var stdout, stderr bytes.Buffer
			args := []string{
				"encode", "--encoding", format.encoding, "--part-size-mib", "1", input,
				filepath.Join(dir, "cancelled"+format.extension),
			}
			if run(ctx, args, &stdout, &stderr) != 1 {
				t.Fatal("cancelled encode succeeded")
			}
			if _, err := os.Stat(filepath.Join(dir, "cancelled.parts")); !os.IsNotExist(err) {
				t.Fatalf("cancelled encode left a set: %v", err)
			}
			stdout.Reset()
			stderr.Reset()
			dest := filepath.Join(dir, "cancelled-out")
			if run(ctx, []string{"decode", parts, dest}, &stdout, &stderr) != 1 {
				t.Fatal("cancelled decode succeeded")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("cancelled decode published destination: %v", err)
			}
		})
	}
}

func TestMultipartCLIAggregateMismatchPublishesNothing(t *testing.T) {
	t.Parallel()
	for _, format := range multipartFormats {
		t.Run(format.name, func(t *testing.T) {
			for _, failure := range []string{"sha256", "crc32"} {
				t.Run(failure, func(t *testing.T) {
					dir := t.TempDir()
					payload := []byte{0, 255, 173, 0}
					part := codec.PartInfo{
						ID: sha256.Sum256(payload), TotalLength: 4, Count: 2, PartSize: 2,
						Checksum: crc32.ChecksumIEEE(payload),
					}
					if failure == "sha256" {
						part.ID[0] ^= 1
					} else {
						part.Checksum ^= 1
					}
					for index := range 2 {
						part.Index = uint32(index)
						part.Offset = uint64(index * 2)
						var v volume.Volume
						var err error
						if format.encoding == "concrete" {
							v, err = codec.EncodeConcretePartContext(
								context.Background(), payload[index*2:index*2+2], "data.bin", layout.Options{}, part,
							)
						} else {
							v, err = codec.EncodeDensePart(payload[index*2:index*2+2], "data.bin", codec.DenseOptions{}, part)
						}
						if err != nil {
							t.Fatal(err)
						}
						path := filepath.Join(dir, []string{"first", "second"}[index]+format.extension)
						writeStructure(t, path, func(w io.Writer) error { return schematic.Write(w, format.extension, v) })
					}
					dest := filepath.Join(dir, "out")
					execute(t, []string{"decode", dir, dest}, 1)
					if _, err := os.Stat(dest); !os.IsNotExist(err) {
						t.Fatalf("aggregate failure published destination: %v", err)
					}
				})
			}
		})
	}
}

func TestMultipartCLIConcreteMixedAdapters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "data.bin")
	payload := []byte{0, 255, 173, 0}
	if err := os.WriteFile(input, payload, 0600); err != nil {
		t.Fatal(err)
	}
	parts, err := encodeParts(
		context.Background(),
		input,
		filepath.Join(dir, "archive.litematic"),
		partsOptions{Encoding: "concrete", Extension: ".litematic", PartSize: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(parts, "000002.litematic")
	v, err := readStructure(second, ".litematic")
	if err != nil {
		t.Fatal(err)
	}
	converted := filepath.Join(parts, "renamed.SCHEMATIC")
	writeStructure(t, converted, func(w io.Writer) error { return schematic.Write(w, ".schematic", v) })
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{parts, converted} {
		dest := t.TempDir()
		execute(t, []string{"decode", source, dest}, 0)
		got, err := os.ReadFile(filepath.Join(dest, "data.bin"))
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("mixed-adapter recovery: %v", err)
		}
	}
}
