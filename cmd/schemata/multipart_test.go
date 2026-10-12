//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
)

func TestMultipartCLIRecoveryAndCollisions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "数据.zip")
	payload := bytes.Repeat([]byte{0, 173, 255, 17, 0}, 420000)
	if err := os.WriteFile(input, payload, 0600); err != nil {
		t.Fatal(err)
	}
	requested := filepath.Join(dir, "archive.litematic")
	execute(t, []string{"encode", "--part-size-mib", "1", input, requested}, 0)
	parts := filepath.Join(dir, "archive.parts")
	entries, err := os.ReadDir(parts)
	if err != nil || len(entries) != 3 {
		t.Fatalf("parts: %v, %v", entries, err)
	}
	// Part identity comes from structure fields, not filenames or listing order.
	if err := os.Rename(filepath.Join(parts, "000001.litematic"), filepath.Join(parts, "z.litematic")); err != nil {
		t.Fatal(err)
	}
	for _, workers := range []string{"1", "4"} {
		dest := filepath.Join(dir, "out")
		execute(t, []string{"decode", "--workers", workers, parts, dest}, 0)
	}
	for _, name := range []string{"数据.zip", "数据 (1).zip"} {
		got, err := os.ReadFile(filepath.Join(dir, "out", name))
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("recovery %s: %v", name, err)
		}
	}
	execute(t, []string{"decode", "--filename", "replacement.bin", filepath.Join(parts, "000002.litematic"), filepath.Join(dir, "replacement")}, 0)
	got, err := os.ReadFile(filepath.Join(dir, "replacement", "replacement.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("part-file recovery: %v", err)
	}
	execute(t, []string{"encode", "--part-size-mib", "1", input, requested}, 0)
	if _, err := os.Stat(filepath.Join(dir, "archive (1).parts")); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartCLIRejectsIncompleteAndMixedSets(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing", "duplicate", "mixed", "corrupt"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "data.bin")
			payload := bytes.Repeat([]byte{0, 255, 19}, 400000)
			if err := os.WriteFile(input, payload, 0600); err != nil {
				t.Fatal(err)
			}
			execute(t, []string{"encode", "--part-size-mib", "1", input, filepath.Join(dir, "archive.litematic")}, 0)
			parts := filepath.Join(dir, "archive.parts")
			second := filepath.Join(parts, "000002.litematic")
			switch failure {
			case "missing":
				if err := os.Remove(second); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				data, err := os.ReadFile(filepath.Join(parts, "000001.litematic"))
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
				execute(t, []string{"encode", "--part-size-mib", "1", input, filepath.Join(dir, "other.litematic")}, 0)
				data, err := os.ReadFile(filepath.Join(dir, "other.parts", "000002.litematic"))
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
			}
			dest := filepath.Join(dir, "out")
			execute(t, []string{"decode", parts, dest}, 1)
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("failed set published destination: %v", err)
			}
		})
	}
}

func TestMultipartCLIEmptyAndCancellation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(input, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	execute(t, []string{"encode", "--part-size-mib", "1", input, filepath.Join(dir, "empty.litematic")}, 0)
	execute(t, []string{"decode", filepath.Join(dir, "empty.parts"), filepath.Join(dir, "out")}, 0)
	got, err := os.ReadFile(filepath.Join(dir, "out", "empty.bin"))
	if err != nil || !bytes.Equal(got, []byte{}) {
		t.Fatalf("empty recovery: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if run(ctx, []string{"encode", "--part-size-mib", "1", input, filepath.Join(dir, "cancelled.litematic")}, &stdout, &stderr) != 1 {
		t.Fatal("cancelled encode succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "cancelled.parts")); !os.IsNotExist(err) {
		t.Fatalf("cancelled encode left a set: %v", err)
	}
}

func TestMultipartCLIAggregateMismatchPublishesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	part := codec.PartInfo{TotalLength: 4, Count: 2, PartSize: 2}
	for index, payload := range [][]byte{{0, 255}, {173, 0}} {
		part.Index = uint32(index)
		part.Offset = uint64(index * 2)
		v, err := codec.EncodeDensePart(payload, "data.bin", codec.DenseOptions{}, part)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, []string{"first.litematic", "second.litematic"}[index])
		writeStructure(t, path, func(w io.Writer) error { return schematic.Write(w, ".litematic", v) })
	}
	dest := filepath.Join(dir, "out")
	execute(t, []string{"decode", dir, dest}, 1)
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("aggregate failure published destination: %v", err)
	}
}
