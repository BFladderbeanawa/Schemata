package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/output"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

type packedOptions struct {
	Layout   layout.Options
	PartSize int
	Forced   bool
	Workers  int
}

// failure records the first error and cancels the shared work context.
type failure struct {
	once   sync.Once
	err    error
	cancel context.CancelFunc
}

func (f *failure) set(err error) {
	f.once.Do(func() {
		f.err = err
		f.cancel()
	})
}

func waitFor(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// encodePacked writes version 6 output: the requested file for a single
// unforced part, otherwise a collision-free .parts directory. Each worker
// reads, hashes, compresses, lays out, and writes its own parts; headers wait
// for part 0's digest and the last part waits for every earlier digest.
func encodePacked(parent context.Context, input, requested string, opts packedOptions) (path string, err error) {
	name := filepath.Base(input)
	if err := output.ValidateName(name); err != nil {
		return "", err
	}
	f, err := os.Open(input)
	if err != nil {
		return "", fmt.Errorf("open input: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat input: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("payload must be a regular file")
	}
	length := uint64(info.Size())
	partSize := uint32(opts.PartSize)
	count, err := codec.PackedPartCount(length, partSize)
	if err != nil {
		return "", err
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	multipart := opts.Forced || count > 1
	if multipart {
		if path, err = reservePartsDirectory(requested); err != nil {
			return "", fmt.Errorf("create parts directory: %w", err)
		}
	}
	var mu sync.Mutex
	created := make([]string, 0, count)
	defer func() {
		if err == nil {
			return
		}
		for _, file := range created {
			err = errors.Join(err, os.Remove(file))
		}
		if multipart {
			err = errors.Join(err, os.Remove(path))
		}
	}()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	fail := failure{cancel: cancel}
	digests := make([][32]byte, count)
	ready := make([]chan struct{}, count)
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	groupReady := make(chan struct{})
	var group [16]byte

	encodeOne := func(compressor *codec.PackedCompressor, index uint32) error {
		offset := uint64(index) * uint64(partSize)
		original := make([]byte, min(uint64(partSize), length-offset))
		if n, readErr := f.ReadAt(original, int64(offset)); n != len(original) {
			return fmt.Errorf("short read, input changed: %w", readErr)
		}
		digest := sha256.Sum256(original)
		digests[index] = digest
		close(ready[index])
		if index == 0 {
			group = codec.PackedGroup(name, length, partSize, count, digest)
			close(groupReady)
		}
		method, data := compressor.Compress(original)
		if err := waitFor(ctx, groupReady); err != nil {
			return err
		}
		h := codec.PackedHeader{
			Method: method, FullLength: length, Offset: offset, Index: index, Count: count,
			PartSize: partSize, DataLength: uint64(len(data)), Checksum: crc32.ChecksumIEEE(original),
			Digest: digest, Group: group,
		}
		if h.Last() {
			for j := range count - 1 {
				if err := waitFor(ctx, ready[j]); err != nil {
					return err
				}
			}
			h.Aggregate = codec.PackedAggregate(digests)
		}
		v, err := codec.EncodePacked(ctx, name, h, data, opts.Layout)
		if err != nil {
			return err
		}
		original, data = nil, nil
		write := func(w io.Writer) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return schematic.Write(w, ".litematic", v)
		}
		var written string
		if multipart {
			written, err = output.Create(path, fmt.Sprintf("%06d.litematic", index+1), write)
		} else {
			written, err = output.Create(filepath.Dir(requested), filepath.Base(requested), write)
		}
		if err != nil {
			return err
		}
		mu.Lock()
		created = append(created, written)
		mu.Unlock()
		return nil
	}

	var next atomic.Uint32
	var wg sync.WaitGroup
	workers := min(opts.Workers, int(count))
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			compressor, err := codec.NewPackedCompressor()
			if err != nil {
				fail.set(err)
				return
			}
			defer compressor.Close()
			for index := next.Add(1) - 1; index < count && ctx.Err() == nil; index = next.Add(1) - 1 {
				if err := encodeOne(compressor, index); err != nil {
					fail.set(fmt.Errorf("encode part %d: %w", index+1, err))
					return
				}
			}
		}()
	}
	wg.Wait()
	if fail.err != nil {
		return path, fail.err
	}
	if err := parent.Err(); err != nil {
		return path, err
	}
	if after, err := f.Stat(); err != nil || uint64(after.Size()) != length {
		return path, errors.Join(errors.New("input size changed while encoding"), err)
	}
	if !multipart {
		path = created[0]
	}
	return path, nil
}

// packedSetPaths returns the files making up the set that holds a packed part.
func packedSetPaths(file string, v volume.Volume) ([]string, error) {
	_, h, err := codec.PeekPacked(v)
	if err != nil {
		return nil, err
	}
	if h.Count == 1 {
		return []string{file}, nil
	}
	return listParts(filepath.Dir(file), ".litematic")
}

// decodePacked validates every part concurrently, writing verified bytes into
// a destination-directory temporary file, and publishes only a complete set.
func decodePacked(
	parent context.Context,
	paths []string,
	destination string,
	replacement string,
	workers int,
) (path string, err error) {
	if err := parent.Err(); err != nil {
		return "", err
	}
	if _, statErr := os.Stat(destination); errors.Is(statErr, os.ErrNotExist) {
		if err := os.MkdirAll(destination, 0755); err != nil {
			return "", fmt.Errorf("create destination directory: %w", err)
		}
		defer func() {
			if err != nil {
				// Removes only the empty directory this invocation created.
				err = errors.Join(err, os.Remove(destination))
			}
		}()
	}
	temp, err := os.CreateTemp(destination, ".schemata-*.partial")
	if err != nil {
		return "", err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, temp.Close())
		}
		err = errors.Join(err, os.Remove(temp.Name()))
	}()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	fail := failure{cancel: cancel}
	var mu sync.Mutex
	var set *codec.PackedHeader
	var name string
	var seen []bool
	var digests [][32]byte
	var aggregate [32]byte

	record := func(part codec.PackedPart) error {
		mu.Lock()
		defer mu.Unlock()
		h := part.Header
		if set == nil {
			if len(paths) != int(h.Count) {
				return fmt.Errorf("packed set needs %d parts, found %d", h.Count, len(paths))
			}
			set, name = &h, part.Name
			seen = make([]bool, h.Count)
			digests = make([][32]byte, h.Count)
		}
		consistent := h.FullLength == set.FullLength && h.PartSize == set.PartSize && h.Count == set.Count
		if !consistent || h.Group != set.Group || part.Name != name {
			return errors.New("mixed or inconsistent packed set")
		}
		if seen[h.Index] {
			return fmt.Errorf("duplicate packed part index %d", h.Index)
		}
		seen[h.Index] = true
		digests[h.Index] = h.Digest
		if h.Last() {
			aggregate = h.Aggregate
		}
		return nil
	}
	decodeOne := func(file string) error {
		v, err := readStructure(file, strings.ToLower(filepath.Ext(file)))
		if err != nil {
			return err
		}
		if v.Packed == nil {
			return errors.New("not a packed version 6 part")
		}
		part, err := codec.DecodePacked(ctx, v)
		if err != nil {
			return err
		}
		if err := record(part); err != nil {
			return err
		}
		if n, err := temp.WriteAt(part.Data, int64(part.Header.Offset)); err != nil || n != len(part.Data) {
			return errors.Join(io.ErrShortWrite, err)
		}
		return nil
	}

	var next atomic.Int64
	var wg sync.WaitGroup
	workers = min(workers, len(paths))
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := next.Add(1) - 1; i < int64(len(paths)) && ctx.Err() == nil; i = next.Add(1) - 1 {
				if err := decodeOne(paths[i]); err != nil {
					fail.set(fmt.Errorf("decode part %q: %w", paths[i], err))
					return
				}
			}
		}()
	}
	wg.Wait()
	if fail.err != nil {
		return "", fail.err
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	for index, ok := range seen {
		if !ok {
			return "", fmt.Errorf("missing packed part index %d", index)
		}
	}
	if codec.PackedAggregate(digests) != aggregate {
		return "", errors.New("packed aggregate digest mismatch")
	}
	if replacement != "" {
		name = replacement
	}
	if err := output.ValidateName(name); err != nil {
		return "", err
	}
	// Truncate covers a trailing empty region; every byte was written by a part.
	if err := temp.Truncate(int64(set.FullLength)); err != nil {
		return "", err
	}
	if err := temp.Sync(); err != nil {
		return "", err
	}
	closed = true
	if err := temp.Close(); err != nil {
		return "", err
	}
	return output.Publish(destination, name, temp.Name())
}
