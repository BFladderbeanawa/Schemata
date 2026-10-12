// Package codec maps file bytes to concrete colors in complete unit order.
package codec

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

const (
	prefixBytes = 23
	unitBytes   = 2048
	unitBlocks  = 4096
	magic       = "SCHEMATA"
	version     = 1
)

// File is a recovered UTF-8 basename and its exact original bytes.
type File struct {
	Name    string
	Payload []byte
}

// Encode validates the filename and capacity before allocating colors.
func Encode(payload []byte, name string, opts layout.Options) (volume.Volume, error) {
	if len(name) > 65535 {
		return volume.Volume{}, fmt.Errorf("filename exceeds 65535 UTF-8 bytes")
	}
	if err := validateName(name); err != nil {
		return volume.Volume{}, fmt.Errorf("encode filename: %w", err)
	}
	maxPayload := volume.MaxBlocks/2 - prefixBytes - len(name)
	if len(payload) > maxPayload {
		return volume.Volume{}, fmt.Errorf("payload exceeds stream capacity of %d bytes", maxPayload)
	}
	used := prefixBytes + len(name) + len(payload)
	v, err := layout.Select((used+unitBytes-1)/unitBytes, opts)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("select layout: %w", err)
	}
	blocks, err := volume.Count(v.X, v.Y, v.Z)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("encode dimensions: %w", err)
	}
	v.Colors = make([]byte, blocks)
	var prefix [prefixBytes]byte
	copy(prefix[:8], magic)
	prefix[8] = version
	binary.BigEndian.PutUint64(prefix[9:17], uint64(len(payload)))
	binary.BigEndian.PutUint16(prefix[17:19], uint16(len(name)))
	binary.BigEndian.PutUint32(prefix[19:23], crc32.ChecksumIEEE(payload))
	for i := range used {
		var b byte
		switch {
		case i < prefixBytes:
			b = prefix[i]
		case i < prefixBytes+len(name):
			b = name[i-prefixBytes]
		default:
			b = payload[i-prefixBytes-len(name)]
		}
		v.Colors[nativeIndex(v, 2*i)] = b >> 4
		v.Colors[nativeIndex(v, 2*i+1)] = b & 15
	}
	return v, nil
}

// Decode validates the entire selection before returning a recovered file.
// Workers write disjoint stream units directly; all are joined before return.
// Callers must not mutate v.Colors while Decode is running.
func Decode(ctx context.Context, v volume.Volume, workers int) (File, error) {
	if ctx == nil {
		return File{}, fmt.Errorf("decode context must not be nil")
	}
	if workers < 1 || workers > 256 {
		return File{}, fmt.Errorf("workers must be from 1 through 256")
	}
	if err := ctx.Err(); err != nil {
		return File{}, fmt.Errorf("decode: %w", err)
	}
	blocks, err := volume.Count(v.X, v.Y, v.Z)
	if err != nil {
		return File{}, fmt.Errorf("decode dimensions: %w", err)
	}
	if len(v.Colors) != blocks {
		return File{}, fmt.Errorf("block count %d does not match volume %d", len(v.Colors), blocks)
	}
	units := blocks / unitBlocks
	workers = min(workers, units)
	stream := make([]byte, blocks/2)
	failures := make([]unitFailure, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := range workers {
		go func(worker int) {
			defer wg.Done()
			for unit := worker; unit < units; unit += workers {
				if ctx.Err() != nil {
					return
				}
				failure := decodeUnit(ctx, v, unit, stream[unit*unitBytes:(unit+1)*unitBytes])
				if failure.err != nil {
					failures[worker] = failure
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return File{}, fmt.Errorf("decode: %w", err)
	}
	// Each worker's units are ascending. Taking the lowest failing nibble
	// therefore gives the same diagnostic for any scheduling/worker count.
	first := unitFailure{}
	for _, failure := range failures {
		if failure.err != nil && (first.err == nil || failure.nibble < first.nibble) {
			first = failure
		}
	}
	if first.err != nil {
		return File{}, first.err
	}
	return parseStream(ctx, stream)
}

type unitFailure struct {
	nibble int
	err    error
}

func decodeUnit(ctx context.Context, v volume.Volume, unit int, dst []byte) unitFailure {
	ux, uz := v.X/16, v.Z/16
	baseY := unit / (ux * uz) * 16
	baseX := unit / uz % ux * 16
	baseZ := unit % uz * 16
	for y := range 16 {
		if err := ctx.Err(); err != nil {
			return unitFailure{err: err}
		}
		for x := range 16 {
			for z := 0; z < 16; z += 2 {
				local := (y*16+x)*16 + z
				index := v.Index(baseX+x, baseY+y, baseZ+z)
				hi, lo := v.Colors[index], v.Colors[index+v.X]
				if hi > 15 {
					return colorFailure(unit*unitBlocks+local, index)
				}
				if lo > 15 {
					return colorFailure(unit*unitBlocks+local+1, index+v.X)
				}
				dst[local/2] = hi<<4 | lo
			}
		}
	}
	return unitFailure{}
}

func colorFailure(nibble, native int) unitFailure {
	return unitFailure{
		nibble: nibble,
		err:    fmt.Errorf("non-concrete color at logical nibble %d (native index %d)", nibble, native),
	}
}

func parseStream(ctx context.Context, stream []byte) (File, error) {
	if string(stream[:8]) != magic {
		return File{}, fmt.Errorf("invalid SCHEMATA header")
	}
	if stream[8] != version {
		return File{}, fmt.Errorf("unsupported encoding version %d", stream[8])
	}
	length := binary.BigEndian.Uint64(stream[9:17])
	nameLength := int(binary.BigEndian.Uint16(stream[17:19]))
	if nameLength > len(stream)-prefixBytes {
		return File{}, fmt.Errorf("filename length exceeds stream capacity")
	}
	start := prefixBytes + nameLength
	// Compare in uint64 before converting an untrusted length to int.
	if length > uint64(len(stream)-start) {
		return File{}, fmt.Errorf("payload length exceeds stream capacity")
	}
	name := string(stream[prefixBytes:start])
	if err := validateName(name); err != nil {
		return File{}, fmt.Errorf("decode filename: %w", err)
	}
	end := start + int(length)
	var checksum uint32
	const chunkBytes = 64 * 1024
	for offset := start; offset < end; offset += chunkBytes {
		if err := ctx.Err(); err != nil {
			return File{}, fmt.Errorf("decode checksum: %w", err)
		}
		checksum = crc32.Update(checksum, crc32.IEEETable, stream[offset:min(end, offset+chunkBytes)])
	}
	if checksum != binary.BigEndian.Uint32(stream[19:23]) {
		return File{}, fmt.Errorf("payload checksum mismatch")
	}
	for offset := end; offset < len(stream); offset += chunkBytes {
		if err := ctx.Err(); err != nil {
			return File{}, fmt.Errorf("decode padding: %w", err)
		}
		for i := offset; i < min(len(stream), offset+chunkBytes); i++ {
			if stream[i] != 0 {
				return File{}, fmt.Errorf("nonzero padding at logical byte %d", i)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return File{}, fmt.Errorf("decode: %w", err)
	}
	return File{Name: name, Payload: stream[start:end:end]}, nil
}

func validateName(name string) error {
	dotName := name == "." || name == ".."
	if name == "" || dotName {
		return fmt.Errorf("filename must be a nonempty basename other than dot or dot-dot")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("filename must be valid UTF-8")
	}
	if strings.ContainsAny(name, "/\\\x00") || filepath.IsAbs(name) {
		return fmt.Errorf("filename must not contain separators, an absolute path, or NUL")
	}
	return nil
}

// nativeIndex converts a logical unit-ordered nibble to x-fastest storage.
func nativeIndex(v volume.Volume, nibble int) int {
	unit, local := nibble/unitBlocks, nibble%unitBlocks
	ux, uz := v.X/16, v.Z/16
	y := unit/(ux*uz)*16 + local/256
	x := unit/uz%ux*16 + local/16%16
	z := unit%uz*16 + local%16
	return v.Index(x, y, z)
}
