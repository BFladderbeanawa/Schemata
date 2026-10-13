package codec

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"

	"github.com/klauspost/compress/zstd"

	"github.com/bfladderbeanawa/schemata/internal/volume"
)

// PackedPart is one validated version 6 part and its original bytes.
type PackedPart struct {
	Name   string
	Header PackedHeader
	Data   []byte
}

// ErrNotPacked reports a structure whose stream does not begin with SCHEMATA.
var ErrNotPacked = errors.New("structure is not packed SCHEMATA data")

// PeekPacked validates and returns a packed part's basename and header
// without reading or decompressing its data section.
func PeekPacked(v volume.Volume) (string, PackedHeader, error) {
	name, h, _, err := parsePacked(v)
	return name, h, err
}

func parsePacked(v volume.Volume) (string, PackedHeader, int, error) {
	if v.Packed == nil {
		return "", PackedHeader{}, 0, ErrNotPacked
	}
	if err := v.Validate(); err != nil {
		return "", PackedHeader{}, 0, fmt.Errorf("packed structure: %w", err)
	}
	capacity := v.X * v.Y * v.Z * volume.PackedBits / 8
	if capacity < packedPrefixBytes {
		return "", PackedHeader{}, 0, errors.New("packed structure too small for header")
	}
	var prefix [packedPrefixBytes]byte
	getStream(prefix[:], v.Packed, 0)
	if string(prefix[:8]) != magic {
		return "", PackedHeader{}, 0, ErrNotPacked
	}
	if prefix[8] != packedVersion {
		return "", PackedHeader{}, 0, fmt.Errorf("unsupported packed encoding version %d", prefix[8])
	}
	nameLength := int(binary.BigEndian.Uint16(prefix[10:12]))
	if nameLength == 0 || nameLength > capacity-packedPrefixBytes {
		return "", PackedHeader{}, 0, fmt.Errorf("packed basename length %d invalid for capacity", nameLength)
	}
	nameBytes := make([]byte, nameLength)
	getStream(nameBytes, v.Packed, packedPrefixBytes)
	crc := crc32.Update(crc32.ChecksumIEEE(prefix[:packedHeaderCRCOffset]), crc32.IEEETable, nameBytes)
	if crc != binary.BigEndian.Uint32(prefix[132:136]) {
		return "", PackedHeader{}, 0, errors.New("packed header checksum mismatch")
	}
	name := string(nameBytes)
	if err := validatePackedName(name); err != nil {
		return "", PackedHeader{}, 0, fmt.Errorf("decode filename: %w", err)
	}
	h := PackedHeader{
		Method:     prefix[9],
		FullLength: binary.BigEndian.Uint64(prefix[12:20]),
		Offset:     binary.BigEndian.Uint64(prefix[20:28]),
		Index:      binary.BigEndian.Uint32(prefix[28:32]),
		Count:      binary.BigEndian.Uint32(prefix[32:36]),
		PartSize:   binary.BigEndian.Uint32(prefix[36:40]),
		DataLength: binary.BigEndian.Uint64(prefix[40:48]),
		Checksum:   binary.BigEndian.Uint32(prefix[48:52]),
	}
	copy(h.Digest[:], prefix[52:84])
	copy(h.Group[:], prefix[84:100])
	copy(h.Aggregate[:], prefix[100:132])
	if err := validatePackedHeader(h); err != nil {
		return "", PackedHeader{}, 0, fmt.Errorf("decode packed header: %w", err)
	}
	start := packedPrefixBytes + nameLength
	if h.DataLength > uint64(capacity-start) {
		return "", PackedHeader{}, 0, errors.New("packed data length exceeds structure capacity")
	}
	return name, h, start, nil
}

// DecodePacked validates one packed part: header CRC, canonical part fields,
// capacity, zero padding, data framing, decompressed length, CRC-32, and
// SHA-256. Group and aggregate consistency across a set is the caller's job.
func DecodePacked(ctx context.Context, v volume.Volume) (PackedPart, error) {
	if ctx == nil {
		return PackedPart{}, errors.New("decode context must not be nil")
	}
	name, h, start, err := parsePacked(v)
	if err != nil {
		return PackedPart{}, err
	}
	end := start + int(h.DataLength)
	if err := checkPackedPadding(v.Packed, end); err != nil {
		return PackedPart{}, err
	}
	if err := ctx.Err(); err != nil {
		return PackedPart{}, fmt.Errorf("decode: %w", err)
	}
	section := make([]byte, h.DataLength)
	getStream(section, v.Packed, start)
	data := section
	if h.Method == PackedZstd {
		if data, err = decompressPacked(section, h.PartLength()); err != nil {
			return PackedPart{}, err
		}
	}
	if crc32.ChecksumIEEE(data) != h.Checksum {
		return PackedPart{}, errors.New("packed part CRC-32 mismatch")
	}
	if sha256.Sum256(data) != h.Digest {
		return PackedPart{}, errors.New("packed part SHA-256 mismatch")
	}
	return PackedPart{Name: name, Header: h, Data: data}, nil
}

// checkPackedPadding requires every LongArray stream byte from end onward to
// be zero; Validate already rejected nonzero unused bits in the final word.
func checkPackedPadding(packed []byte, end int) error {
	b := end
	for ; b < len(packed) && b&7 != 0; b++ {
		if packed[streamIndex(b)] != 0 {
			return fmt.Errorf("nonzero packed padding at stream byte %d", b)
		}
	}
	for ; b < len(packed); b += 8 {
		if binary.BigEndian.Uint64(packed[b:]) != 0 {
			return fmt.Errorf("nonzero packed padding in stream word %d", b/8)
		}
	}
	return nil
}

// checkZstdFrame enforces exactly one RFC 8878 frame with no dictionary,
// checksum, trailing bytes, wrong content size, or over-limit window.
func checkZstdFrame(data []byte, p uint64) error {
	var h zstd.Header
	if err := h.Decode(data); err != nil {
		return fmt.Errorf("invalid zstd frame header: %w", err)
	}
	switch {
	case h.Skippable:
		return errors.New("zstd skippable frames are not permitted")
	case h.DictionaryID != 0:
		return errors.New("zstd dictionaries are not permitted")
	case h.HasCheckSum:
		return errors.New("zstd content checksum flag must be clear")
	case h.HasFCS && h.FrameContentSize != p:
		return fmt.Errorf("zstd content size %d differs from part length %d", h.FrameContentSize, p)
	case !h.SingleSegment && h.WindowSize > packedMaxWindow:
		return fmt.Errorf("zstd window %d exceeds %d", h.WindowSize, packedMaxWindow)
	}
	pos := h.HeaderSize
	for {
		if len(data)-pos < 3 {
			return errors.New("truncated zstd block header")
		}
		header := uint32(data[pos]) | uint32(data[pos+1])<<8 | uint32(data[pos+2])<<16
		pos += 3
		size := int(header >> 3)
		switch (header >> 1) & 3 {
		case 1: // RLE block stores one byte.
			size = 1
		case 3:
			return errors.New("reserved zstd block type")
		}
		if size > len(data)-pos {
			return errors.New("truncated zstd block")
		}
		pos += size
		if header&1 != 0 {
			break
		}
	}
	if pos != len(data) {
		return errors.New("trailing data after zstd frame")
	}
	return nil
}

func decompressPacked(section []byte, p uint64) ([]byte, error) {
	if err := checkZstdFrame(section, p); err != nil {
		return nil, err
	}
	// Output is capped at cap(dst) = p. The window/history limit is the part
	// length, floored at twice the 1 KiB zstd minimum because the encoder
	// rounds tiny non-single-segment windows up to the next power of two.
	// checkZstdFrame already capped non-single-segment windows at 8 MiB.
	limit := max(p, 2*zstd.MinWindowSize)
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(limit),
		zstd.WithDecoderMaxWindow(limit),
		zstd.WithDecodeAllCapLimit(true),
	)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	out, err := dec.DecodeAll(section, make([]byte, 0, p))
	if err != nil {
		return nil, fmt.Errorf("zstd decode: %w", err)
	}
	if uint64(len(out)) != p {
		return nil, fmt.Errorf("zstd decoded %d bytes, want %d", len(out), p)
	}
	return out, nil
}
