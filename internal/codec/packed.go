package codec

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

// Packed version 6 constants; see docs/packed-v6.md.
const (
	packedVersion         = 6
	packedPrefixBytes     = 136
	packedHeaderCRCOffset = 132
	MaxPackedPartSize     = 79 * 1024 * 1024
	DefaultPackedPartSize = 64 * 1024 * 1024
	MaxPackedParts        = 4096
	PackedStored          = 0
	PackedZstd            = 1
	packedMaxWindow       = 8 * 1024 * 1024
)

// PackedHeader is the fixed version 6 header, excluding the basename.
type PackedHeader struct {
	Method     byte
	FullLength uint64
	Offset     uint64
	Index      uint32
	Count      uint32
	PartSize   uint32
	DataLength uint64
	Checksum   uint32
	Digest     [32]byte
	Group      [16]byte
	Aggregate  [32]byte
}

// PartLength is the original byte count of this part.
func (h PackedHeader) PartLength() uint64 {
	return min(uint64(h.PartSize), h.FullLength-h.Offset)
}

// Last reports whether this part carries the aggregate digest.
func (h PackedHeader) Last() bool { return h.Index == h.Count-1 }

// PackedPartCount returns the canonical part count, or an error when the
// part size or resulting count is outside the version 6 limits.
func PackedPartCount(fullLength uint64, partSize uint32) (uint32, error) {
	if partSize < 1 || partSize > MaxPackedPartSize {
		return 0, fmt.Errorf("packed part size %d outside 1..%d", partSize, MaxPackedPartSize)
	}
	count := max(1, fullLength/uint64(partSize)+min(1, fullLength%uint64(partSize)))
	if count > MaxPackedParts {
		return 0, fmt.Errorf("packed input requires %d parts, limit is %d", count, MaxPackedParts)
	}
	return uint32(count), nil
}

func validatePackedHeader(h PackedHeader) error {
	count, err := PackedPartCount(h.FullLength, h.PartSize)
	if err != nil {
		return err
	}
	if h.Count != count {
		return fmt.Errorf("packed part count %d is not canonical %d", h.Count, count)
	}
	if h.Index >= h.Count {
		return fmt.Errorf("packed part index %d outside count %d", h.Index, h.Count)
	}
	// Index < 4096 and part size < 2^27 cannot overflow uint64.
	if h.Offset != uint64(h.Index)*uint64(h.PartSize) || (h.Offset >= h.FullLength && h.FullLength != 0) {
		return fmt.Errorf("packed part offset is not canonical")
	}
	if !h.Last() && h.Aggregate != [32]byte{} {
		return fmt.Errorf("packed aggregate digest present before the last part")
	}
	p := h.PartLength()
	switch h.Method {
	case PackedStored:
		if h.DataLength != p {
			return fmt.Errorf("stored packed data length %d differs from part length %d", h.DataLength, p)
		}
	case PackedZstd:
		if h.DataLength == 0 || h.DataLength >= p {
			return fmt.Errorf("zstd packed data length %d is not within 1..%d", h.DataLength, max(p, 1)-1)
		}
	default:
		return fmt.Errorf("unknown packed data method %d", h.Method)
	}
	return nil
}

// PackedGroup derives the deterministic set identifier from part 0's digest.
func PackedGroup(name string, fullLength uint64, partSize, count uint32, digest0 [32]byte) [16]byte {
	h := sha256.New()
	h.Write([]byte("SCHEMATA packed v6 group"))
	var fields [18]byte
	binary.BigEndian.PutUint64(fields[0:8], fullLength)
	binary.BigEndian.PutUint32(fields[8:12], partSize)
	binary.BigEndian.PutUint32(fields[12:16], count)
	binary.BigEndian.PutUint16(fields[16:18], uint16(len(name)))
	h.Write(fields[:])
	h.Write([]byte(name))
	h.Write(digest0[:])
	var group [16]byte
	copy(group[:], h.Sum(nil))
	return group
}

// PackedAggregate is SHA-256 over per-part digests in index order.
func PackedAggregate(digests [][32]byte) [32]byte {
	h := sha256.New()
	for _, d := range digests {
		h.Write(d[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// PackedCompressor owns one single-threaded pinned zstd encoder. It is not
// safe for concurrent use; give each worker its own compressor.
type PackedCompressor struct {
	enc *zstd.Encoder
	dst []byte
}

// NewPackedCompressor pins SpeedBestCompression, concurrency 1, no CRC.
func NewPackedCompressor() (*PackedCompressor, error) {
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderCRC(false),
	)
	if err != nil {
		return nil, err
	}
	return &PackedCompressor{enc: enc}, nil
}

// Compress returns the method and data section for part. The returned data
// aliases part (stored) or the compressor's reused buffer (zstd), and is
// valid until the next Compress call.
func (c *PackedCompressor) Compress(part []byte) (byte, []byte) {
	if len(part) == 0 {
		return PackedStored, part
	}
	c.dst = c.enc.EncodeAll(part, c.dst[:0])
	if len(c.dst) >= len(part) {
		return PackedStored, part
	}
	return PackedZstd, c.dst
}

// Close releases encoder resources.
func (c *PackedCompressor) Close() error { return c.enc.Close() }

func marshalPackedHeader(h PackedHeader, name string) [packedPrefixBytes]byte {
	var b [packedPrefixBytes]byte
	copy(b[:8], magic)
	b[8] = packedVersion
	b[9] = h.Method
	binary.BigEndian.PutUint16(b[10:12], uint16(len(name)))
	binary.BigEndian.PutUint64(b[12:20], h.FullLength)
	binary.BigEndian.PutUint64(b[20:28], h.Offset)
	binary.BigEndian.PutUint32(b[28:32], h.Index)
	binary.BigEndian.PutUint32(b[32:36], h.Count)
	binary.BigEndian.PutUint32(b[36:40], h.PartSize)
	binary.BigEndian.PutUint64(b[40:48], h.DataLength)
	binary.BigEndian.PutUint32(b[48:52], h.Checksum)
	copy(b[52:84], h.Digest[:])
	copy(b[84:100], h.Group[:])
	copy(b[100:132], h.Aggregate[:])
	crc := crc32.Update(crc32.ChecksumIEEE(b[:packedHeaderCRCOffset]), crc32.IEEETable, []byte(name))
	binary.BigEndian.PutUint32(b[132:136], crc)
	return b
}

func validatePackedName(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if len(name) > 65535 {
		return fmt.Errorf("filename exceeds 65535 UTF-8 bytes")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("filename must be valid UTF-8")
	}
	return nil
}

// PackedRequiredPositions returns ceil(8*|S|/10), or an error above 64 Mi.
func PackedRequiredPositions(nameBytes int, dataLength uint64) (int, error) {
	if dataLength > MaxPackedPartSize {
		return 0, fmt.Errorf("packed data section exceeds %d bytes", MaxPackedPartSize)
	}
	streamBits := 8 * (uint64(packedPrefixBytes) + uint64(nameBytes) + dataLength)
	required := (streamBits + volume.PackedBits - 1) / volume.PackedBits
	if required > volume.MaxBlocks {
		return 0, fmt.Errorf("packed stream requires %d positions, limit is %d", required, volume.MaxBlocks)
	}
	return int(required), nil
}

// EncodePacked lays out one version 6 part. h.DataLength and h.Method must
// describe data; CRC-32, SHA-256, group, and aggregate are supplied by the
// caller, which owns the original part bytes. Decoding verifies all of them.
func EncodePacked(ctx context.Context, name string, h PackedHeader, data []byte, opts layout.Options) (volume.Volume, error) {
	if err := validatePackedName(name); err != nil {
		return volume.Volume{}, fmt.Errorf("encode filename: %w", err)
	}
	if h.DataLength != uint64(len(data)) {
		return volume.Volume{}, errors.New("packed data length does not match data section")
	}
	if err := validatePackedHeader(h); err != nil {
		return volume.Volume{}, fmt.Errorf("encode packed header: %w", err)
	}
	required, err := PackedRequiredPositions(len(name), h.DataLength)
	if err != nil {
		return volume.Volume{}, err
	}
	v, err := layout.SelectDense(required, opts)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("select packed layout: %w", err)
	}
	n, err := volume.CountDense(v.X, v.Y, v.Z)
	if err != nil {
		return volume.Volume{}, err
	}
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, fmt.Errorf("encode: %w", err)
	}
	v.Packed = make([]byte, volume.PackedBytes(n))
	header := marshalPackedHeader(h, name)
	putStream(v.Packed, 0, header[:])
	putStream(v.Packed, packedPrefixBytes, []byte(name))
	putStream(v.Packed, packedPrefixBytes+len(name), data)
	return v, nil
}

// Stream byte b lives in LongArray word b/8, whose little-endian value is
// S[8j..8j+7]. Words are serialized big-endian, so the byte is reversed
// within its 8-byte group.
func streamIndex(b int) int { return b&^7 | (7 - b&7) }

func putStream(packed []byte, offset int, src []byte) {
	i := 0
	for ; i < len(src) && (offset+i)&7 != 0; i++ {
		packed[streamIndex(offset+i)] = src[i]
	}
	for ; i+8 <= len(src); i += 8 {
		binary.BigEndian.PutUint64(packed[offset+i:], binary.LittleEndian.Uint64(src[i:]))
	}
	for ; i < len(src); i++ {
		packed[streamIndex(offset+i)] = src[i]
	}
}

func getStream(dst, packed []byte, offset int) {
	i := 0
	for ; i < len(dst) && (offset+i)&7 != 0; i++ {
		dst[i] = packed[streamIndex(offset+i)]
	}
	for ; i+8 <= len(dst); i += 8 {
		binary.LittleEndian.PutUint64(dst[i:], binary.BigEndian.Uint64(packed[offset+i:]))
	}
	for ; i < len(dst); i++ {
		dst[i] = packed[streamIndex(offset+i)]
	}
}
