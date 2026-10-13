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
	"slices"
	"strings"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/output"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

const maxParts = 4096

type partsOptions struct {
	Encoding  string
	Extension string
	Layout    layout.Options
	Banners   bool
	PartSize  int
}

func needsMultipart(length int64, name, encoding string, explicit bool) bool {
	capacity := codec.MaxDensePayload
	if encoding == "concrete" {
		capacity = codec.ConcretePayloadCapacity(name, false)
	}
	return explicit || length > int64(capacity)
}

func partPayloadSize(name, encoding string, requested int) int {
	if encoding == "concrete" {
		return min(requested, codec.ConcretePayloadCapacity(name, true))
	}
	return requested
}

// copyContext checks cancellation between bounded I/O operations.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 256*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, err := dst.Write(buffer[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func reservePartsDirectory(path string) (string, error) {
	dir := filepath.Dir(path)
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for suffix := 0; ; suffix++ {
		name := stem + ".parts"
		if suffix > 0 {
			name = fmt.Sprintf("%s (%d).parts", stem, suffix)
		}
		if err := output.ValidateName(name); err != nil {
			return "", err
		}
		candidate := filepath.Join(dir, name)
		err := os.Mkdir(candidate, 0700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return candidate, err
	}
}

func encodeParts(
	ctx context.Context,
	input string,
	requested string,
	opts partsOptions,
) (path string, err error) {
	if opts.PartSize < 1 || opts.PartSize > codec.MaxDensePayload {
		return "", errors.New("multipart part size outside payload resource limit")
	}
	name := filepath.Base(input)
	if err := output.ValidateName(name); err != nil {
		return "", err
	}
	partSize := partPayloadSize(name, opts.Encoding, opts.PartSize)
	f, err := os.Open(input)
	if err != nil {
		return "", fmt.Errorf("open input: %w", err)
	}
	defer func() {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat input: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("payload must be a regular file")
	}
	count := info.Size() / int64(partSize)
	if info.Size()%int64(partSize) != 0 {
		count++
	}
	count = max(int64(1), count)
	if count > maxParts {
		return "", fmt.Errorf("multipart input requires %d parts, limit is %d", count, maxParts)
	}
	hash := sha256.New()
	crc := crc32.NewIEEE()
	length, err := copyContext(ctx, io.MultiWriter(hash, crc), io.LimitReader(f, info.Size()+1))
	if err != nil {
		return "", fmt.Errorf("hash input: %w", err)
	}
	if length != info.Size() {
		return "", errors.New("input size changed while hashing")
	}
	part := codec.PartInfo{TotalLength: uint64(length), Count: uint32(count), Checksum: crc.Sum32(), PartSize: uint32(partSize)}
	copy(part.ID[:], hash.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind input: %w", err)
	}
	path, err = reservePartsDirectory(requested)
	if err != nil {
		return "", fmt.Errorf("create parts directory: %w", err)
	}
	created := make([]string, 0, count)
	defer func() {
		if err != nil {
			for _, file := range created {
				err = errors.Join(err, os.Remove(file))
			}
			err = errors.Join(err, os.Remove(path))
		}
	}()
	hash.Reset()
	crc.Reset()
	buffer := make([]byte, min(int64(partSize), length))
	for index := uint32(0); index < part.Count; index++ {
		if err = ctx.Err(); err != nil {
			return path, err
		}
		part.Index = index
		part.Offset = uint64(index) * uint64(partSize)
		size := min(uint64(partSize), part.TotalLength-part.Offset)
		payload := buffer[:int(size)]
		if _, err = io.ReadFull(f, payload); err != nil {
			return path, fmt.Errorf("read part %d: %w", index+1, err)
		}
		if _, err = io.MultiWriter(hash, crc).Write(payload); err != nil {
			return path, err
		}
		var v volume.Volume
		var encodeErr error
		if opts.Encoding == "concrete" {
			v, encodeErr = codec.EncodeConcretePartContext(ctx, payload, name, opts.Layout, part)
		} else {
			v, encodeErr = codec.EncodeDensePartContext(ctx, payload, name, codec.DenseOptions{
				Layout: opts.Layout, Banners: opts.Banners,
			}, part)
		}
		if encodeErr != nil {
			return path, fmt.Errorf("encode part %d: %w", index+1, encodeErr)
		}
		filename := fmt.Sprintf("%06d%s", index+1, opts.Extension)
		written, writeErr := output.Create(path, filename, func(w io.Writer) error {
			return schematic.Write(w, opts.Extension, v)
		})
		if writeErr != nil {
			return path, fmt.Errorf("write part %d: %w", index+1, writeErr)
		}
		created = append(created, written)
	}
	var extra [1]byte
	if n, readErr := f.Read(extra[:]); n != 0 || !errors.Is(readErr, io.EOF) {
		return path, errors.New("input grew while encoding")
	}
	var id [32]byte
	copy(id[:], hash.Sum(nil))
	if id != part.ID || crc.Sum32() != part.Checksum {
		return path, errors.New("input changed while encoding")
	}
	if err = ctx.Err(); err != nil {
		return path, err
	}
	if err = f.Close(); err != nil {
		return path, fmt.Errorf("close input: %w", err)
	}
	f = nil
	return path, nil
}

func decodeParts(
	ctx context.Context,
	input string,
	destination string,
	replacement string,
	workers int,
) (path string, err error) {
	info, err := os.Stat(input)
	if err != nil {
		return "", fmt.Errorf("stat multipart input: %w", err)
	}
	directory := input
	if !info.IsDir() {
		directory = filepath.Dir(input)
	}
	paths, err := listParts(directory, ".litematic", ".schematic")
	if err != nil {
		return "", err
	}
	// Packed sets are identified by their in-structure stream, not names.
	for _, p := range paths {
		if strings.ToLower(filepath.Ext(p)) != ".litematic" {
			continue
		}
		if v, readErr := readStructure(p, ".litematic"); readErr == nil && v.Packed != nil {
			packedPaths, err := listParts(directory, ".litematic")
			if err != nil {
				return "", err
			}
			return decodePacked(ctx, packedPaths, destination, replacement, workers)
		}
		break
	}
	temp, err := os.CreateTemp("", "schemata-recovery-*")
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, temp.Close(), os.Remove(temp.Name())) }()
	seen := make([]bool, maxParts)
	var set *codec.PartInfo
	var name string
	for _, filePath := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		v, err := readStructure(filePath, strings.ToLower(filepath.Ext(filePath)))
		if err != nil {
			return "", fmt.Errorf("read part %q: %w", filePath, err)
		}
		file, err := codec.Decode(ctx, v, workers)
		if err != nil {
			return "", fmt.Errorf("decode part %q: %w", filePath, err)
		}
		if file.Part == nil {
			return "", errors.New("multipart directory contains a single-volume file")
		}
		part := file.Part
		if set == nil {
			set = part
			name = file.Name
			if len(paths) != int(part.Count) {
				return "", fmt.Errorf("multipart set needs %d parts, found %d", part.Count, len(paths))
			}
		}
		consistent := part.ID == set.ID && part.TotalLength == set.TotalLength && part.Count == set.Count
		consistent = consistent && part.Checksum == set.Checksum && part.PartSize == set.PartSize && file.Name == name
		if !consistent {
			return "", errors.New("mixed or inconsistent multipart set")
		}
		if seen[part.Index] {
			return "", fmt.Errorf("duplicate multipart index %d", part.Index)
		}
		seen[part.Index] = true
		if _, err := temp.Seek(int64(part.Offset), io.SeekStart); err != nil {
			return "", err
		}
		written, err := temp.Write(file.Payload)
		if err != nil {
			return "", err
		}
		if written != len(file.Payload) {
			return "", io.ErrShortWrite
		}
	}
	for index := uint32(0); index < set.Count; index++ {
		if !seen[index] {
			return "", fmt.Errorf("missing multipart index %d", index)
		}
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	crc := crc32.NewIEEE()
	length, err := copyContext(ctx, io.MultiWriter(hash, crc), temp)
	if err != nil {
		return "", err
	}
	var id [32]byte
	copy(id[:], hash.Sum(nil))
	if uint64(length) != set.TotalLength || id != set.ID || crc.Sum32() != set.Checksum {
		return "", errors.New("multipart full-file checksum mismatch")
	}
	if replacement != "" {
		name = replacement
	}
	if err := output.ValidateName(name); err != nil {
		return "", err
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		return "", err
	}
	return output.Create(destination, name, func(w io.Writer) error {
		_, err := copyContext(ctx, w, temp)
		return err
	})
}

// listParts enumerates at most maxParts regular files with the given
// lowercase extensions; names and listing order never identify parts.
func listParts(directory string, exts ...string) (paths []string, err error) {
	dir, err := os.Open(directory)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if !slices.Contains(exts, strings.ToLower(filepath.Ext(entry.Name()))) {
				continue
			}
			if !entry.Type().IsRegular() {
				return nil, errors.New("multipart entries must be regular files")
			}
			paths = append(paths, filepath.Join(directory, entry.Name()))
			if len(paths) > maxParts {
				return nil, errors.New("multipart directory exceeds 4096 parts")
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("multipart directory contains no supported schematic parts")
	}
	return paths, nil
}
