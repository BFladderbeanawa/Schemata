# AGENTS.md

## Purpose and status

Schemata is a Go CLI that stores arbitrary file bytes and their UTF-8 basename in Minecraft schematic structures and recovers them exactly.

This document defines required behavior, not a claim of implemented or verified functionality. The repository contains concrete and dense implementations. The packed encoding (version 6) described here is an expansion requirement; do not describe it as available until implemented and exercised.

Use explicit `encode` and `decode` commands. Do not infer the operation from the input extension or inspect payload content to choose an operation. A schematic file can itself be the payload.

## Encoding modes and guarantees

| Mode | Supported output | Default | Recovery guarantee |
| --- | --- | --- | --- |
| `packed` | `.litematic` only | `.litematic` | Exact recovery through Schemata's own writer and reader. Output must remain loadable by Litematica; no world-placement recovery guarantee. |
| `dense` | `.litematic` only | None; explicit `--encoding dense` | Exact recovery through Schemata's own writer and reader; no external-tool re-save or world-placement guarantee. |
| `concrete` | `.litematic` and `.schematic` | `.schematic` | Existing concrete encoding, including translation without rotation or mirroring. Preserve its placement-oriented requirements; do not expand compatibility claims without evidence. |

Retain `--encoding dense` and `--encoding concrete` for `.litematic`. Reject an explicit packed or dense request for `.schematic`; never silently downgrade it. Continue decoding existing concrete- and dense-encoded files without requiring users to select an encoding mode.

Dense encoding is schematic-only. Registered states may be unstable, transient, unsupported by neighboring blocks, or impossible to maintain in a normal world. Do not promise recovery after placement, ticking, chunk reload, world export, or external-tool re-saving. External tools are not required to implement or verify this expansion.

Dense optimizes payload density per block position, not smaller compressed schematic files. Banner NBT and larger palettes may increase file size. Bounded-chunk packing accepts rounding overhead; do not claim globally optimal packing or maximum speed without evidence.

Packed optimizes on-disk file size. It compresses the payload with zstd and maps the compressed bits onto a fixed palette of full opaque cubes, so the outer gzip sees nearly incompressible data with no radix-conversion loss. File size is bounded below by the payload's entropy; already-compressed input does not shrink. Do not claim compression gains without measured evidence.

## Supported schematic formats

Read and write:

- Litematica `.litematic`, not `.litematica`. Reference: https://github.com/maruohon/litematica
- Legacy MCEdit `.schematic`, using numeric block IDs and metadata for concrete. Compatibility targets modern tools that import this legacy format, not Minecraft 1.12 worlds.

Do not support Sponge `.schem` or multi-region Litematica files. Export exactly one Litematica region; reject multi-region input rather than selecting or merging regions.

Retain the existing pinned adapter contracts:

- Litematica: gzip-compressed big-endian NBT, Version 6, SubVersion 1. Also read Version 6 with absent or zero SubVersion. Dense and packed output use MinecraftDataVersion 3465, Java 1.20.1. Block-state packing is continuous, least-significant bits first, with `max(2, ceil(log2(palette size)))` bits per entry, including entries crossing 64-bit words. Native coordinate order is x-fastest, then z, then y.
- Legacy MCEdit: gzip-compressed big-endian NBT rooted at `Schematic`, `Materials="Alpha"`, signed-short Width/Height/Length, concrete block ID 251, metadata 0–15. This format has no numbered version field; reject other Materials variants and explicit Version tags. Native index is `(y*Length+z)*Width+x`. Validate optional AddBlocks length and reject nonzero high-ID bits for selected concrete blocks.

Verify adapter behavior against actual format specifications. External fixtures may provide additional evidence but are not a required acceptance gate. The Litematica repository is not a specification for legacy `.schematic`. Reject unsupported versions explicitly.

Keep schematic adapters separate from encoding and layout logic. Palette indices, native storage order, NBT compound-key order, and runtime Minecraft registry IDs are not encoding symbols. Concrete output in both formats must represent the same logical colors, coordinates, and byte stream.

## CLI and output files

Required interface:

- `encode`: input file, output schematic path, `--encoding`, `--max-height`, `--layout`, the bounded-layout padding option, `--part-size-mib`, and `--workers`. Select the schematic format from `.litematic` or `.schematic`; reject unsupported extensions.
- `decode`: input schematic, destination directory, optional `--filename`, and `--workers`. Detect the Schemata encoding from its in-structure identifier/version, not payload content or user recollection of encode settings.
- `--workers` accepts 1–256 for both commands and defaults to `min(GOMAXPROCS, 256)`. Worker count never changes encoded logical output.
- Packed, dense, and concrete all default to `balanced` layout, prioritizing a three-dimensional rectangular shape. An explicit layout overrides the default; `compact` remains available for minimum-position/unit output and can produce long, thin rectangles.
- Flags precede positional arguments. Report actual output paths. Preserve the original extension as part of the stored basename.

Never overwrite an existing destination, for either command. On a collision, append a numeric suffix before the final extension: `report.pdf`, `report (1).pdf`, `report (2).pdf`. A user-selected filename follows the same policy. Destination selection must remain no-overwrite if another process creates a file concurrently.

Store filenames as UTF-8 basenames only, never source directory paths. Treat header filenames as untrusted: reject path separators, absolute paths, and `.`/`..` traversal names. Reject names that cannot be represented safely on the destination filesystem unless the user supplies a valid replacement filename. A replacement does not permit structural traversal names in the stored header. Never write outside the chosen destination directory.

Validate the complete structure, header, declared lengths, payload checksum, and required padding before publishing a recovered file. Worker or write failure must not leave a file that appears to be a successful recovery. Remove incomplete output on failure.

## Dense alphabet: vanilla Java 1.20.1

Include the complete registered vanilla Java 1.20.1 block-state catalog, not a curated subset. Every registered complete state is eligible, including:

- Block identity and all declared property values.
- Directional properties such as `facing`, `axis`, and `rotation`.
- Power, connections, waterlogging, tilt, and other registered properties, irrespective of world stability.
- `minecraft:air`, `minecraft:cave_air`, and `minecraft:void_air` as three distinct symbols.

Do not assume state counts from examples. Obtain the pinned catalog from authoritative Java 1.20.1 data, record its provenance, and validate it independently of the writer.

Extend banner states with native ordered pattern/color lists containing zero through six pattern layers. Include the banner's base color and applicable state properties. Use the vanilla 1.20.1 pattern catalog; do not include modded or data-pack-defined patterns. Pin which native pattern values are accepted before freezing the encoding.

Serialized Minecraft distinctions count even when visually indistinguishable. Repeated patterns and obscured layers may carry data. Banner layer order matters; NBT compound-key order does not. Do not deduplicate symbols by rendered appearance.

Only native banner pattern data is an additional payload carrier. Arbitrary custom NBT, names, text, inventories, and other block-entity contents must not carry payload. A chest's facing may encode data; its inventory may not. Define canonical non-payload block-entity data where needed for compatible serialization, without introducing hidden payload fields.

Define one canonical ordering of complete block-state tuples and banner combinations, independent of file palette order and runtime registry IDs. Ordinary states and patterned banners belong to the same alphabet; do not silently omit ordinary states merely because banners provide greater capacity. Account for an unpatterned banner only once, rather than duplicating equivalent representations.

Use compact ranking/unranking for the combinatorial banner alphabet. Never materialize every banner combination or allocate a lookup table proportional to that entire alphabet. Check symbol-rank arithmetic and reject states, properties, patterns, colors, or layer counts outside the pinned encoding contract.

## Dense wire format and chunks

Use a new encoding version; do not reinterpret concrete version 1. Freeze and document the dense wire specification before implementing its codec. Required decisions include:

- Exact identifier, version, field widths, byte order, and encoding dispatch rules.
- Canonical alphabet definition and cardinality, with compact rank/unrank rules.
- One fixed header/bootstrap strategy; no user-configurable header strategy.
- Logical coordinate traversal, chunk byte size, symbol counts, byte-to-symbol conversion, and inverse conversion.
- Final short-chunk framing, leading-zero preservation, unused numeric combinations, and padding representation.
- Rules distinguishing corruption from valid alternate serialization.

Prefer a short concrete-encoded bootstrap for unambiguous dispatch, followed by dense data. Specify its exact representation and coordinate traversal rather than assuming the concrete codec's 16-cubed traversal applies to arbitrary dense dimensions.

Keep identification, filename, payload length, and integrity fields in the structure itself, including native banner data where specified. Do not store required recovery information solely in custom schematic metadata or a sidecar. The decoder must recognize both concrete and dense formats without an encoding flag, including unsupported versions that must be rejected.

Use bounded, independently decodable chunks. Chunk boundaries and conversion rules are fixed by the encoding version, not worker count, scheduling, CPU count, or layout dimensions. Use the full alphabet in the conversion; do not restrict it to a power-of-two subset and thereby make catalog entries unreachable.

For each chunk, define deterministic reversible conversion between bytes and base-N symbols, where N is the pinned alphabet cardinality. Preserve leading and trailing zero bytes. Validate declared lengths and numeric ranges before allocating or converting. Accept the documented per-chunk rounding overhead instead of whole-file unbounded integer conversion.

Dense encoding must balance banner density against native NBT resources. When another full-alphabet chunk would exhaust the banner budget, fall back within dense mode to a documented ordinary nonbanner-state chunk alphabet instead of failing solely on banner-node exhaustion. Record each chunk's radix choice in the structure under a new encoding version; keep logical byte boundaries deterministic and retain decoding of earlier versions. Full-alphabet chunks keep every registered symbol eligible; the fallback must not silently switch to concrete or raise independent resource limits.

Use one file header containing the original payload length, UTF-8 filename byte length and basename, and CRC-32/IEEE over payload bytes only. Exclude header and padding from CRC32. Support empty files and recover exactly the declared payload length; never trim real zero bytes.

Dense padding is a specified canonical representation, not necessarily white concrete. Air is data, not a missing position or end marker. Validate padding rather than inferring payload length from occupied blocks.

Reject malformed headers, unsupported encoding versions, impossible lengths, truncated chunks or payloads, invalid symbols, invalid banner data, checksum mismatches, and invalid padding. CRC32 detects accidental corruption; it is not authentication, error correction, or protection against deliberate tampering.

## Packed version 6: palette, wire format, and frames

Packed is a separate encoding under version 6, frozen in `docs/packed-v6.md`; that document is authoritative for field widths, byte order, capacity, dispatch, and rejection rules. Do not reinterpret concrete versions 1/5 or dense versions 2–4. The dense full-catalog and non-power-of-two rules do not apply to packed.

Palette: list every Java 1.20.1 complete state with `isSolidRender(EmptyBlockGetter.INSTANCE, BlockPos.ZERO)` true and `hasBlockEntity()` false. Probing the official 1.20.1 server jar found 1,989 such states across 382 blocks, all with render shape `MODEL`. Take the first 1,024 in canonical catalog order (`internal/alphabet/blocks.tsv` block order, then property expansion order); they span 217 blocks, with `note_block` at symbols 454–1023. Symbol `i` is the i-th selected state, so `k = 10` bits per position. Commit the selected list as data, generate it with the pinned-jar probe in `internal/alphabet/generate/`, record jar hashes in `PROVENANCE.md`, and test the 1,989/1,024 counts and the list digest pinned in the specification independently of the writer.

Every packed volume writes exactly these 1,024 palette entries in canonical order, so the Litematica bit width is 10 and the LongArray holds the stream bits directly. The reader maps positions to symbols by state identity, never by palette index; palette reordering, pruning, and unused valid entries do not affect recovery. Reject any position whose state is outside the selected set, and reject block entities, entities, and ticks.

Identification is in-stream. There is no concrete bootstrap. The stream begins with `SCHEMATA` and encoding version 6, followed by a fixed header, the basename, and the data section. Dispatch tries concrete and dense first, then packed; reject unsupported versions. Concrete and dense identifiers cannot match packed data, because every byte of `SCHEMATA` contains nibble 4 (yellow concrete), which is not a selected state.

Header fields: data method, basename, full-file length, part offset, index, count, and size, data length, per-part CRC-32/IEEE and SHA-256 over original part bytes, a deterministic group identifier, the aggregate digest in the last part, and a header CRC. The aggregate digest is SHA-256 over the concatenated per-part SHA-256 values in index order. The group identifier is derived from the basename, lengths, and part 0's digest; it only groups parts and is not integrity.

Compression: `github.com/klauspost/compress/zstd` v1.18.0, `SpeedBestCompression`, encoder concurrency 1, frame checksum disabled. The level is not user-configurable. Use the stored method when zstd output is not shorter than the part. Decoders must bound decompressed output to the declared part length.

Frames: exactly one zstd frame per part, with no intra-part frame table. Parallelism comes from processing parts concurrently; a single-part file compresses on one thread. Rationale from benchmark evidence (klauspost v1.18.0, 64 MiB corpora, single run): 1 MiB frames cost +7.4% (Go source) and +46% (Go binaries) versus one frame; 16 MiB frames cost +0.8% and +5.6%. Single-thread speed was 17–30 MB/s regardless of frame size. Do not split parts into frames without new evidence and a new encoding version.

Padding after the compressed data and header is symbol 0 to the end of the region; reject any nonzero padding. Packed is `.litematic` only.

## Concrete encoding compatibility

Preserve the existing version 1 byte stream. Numeric header fields are unsigned big-endian:

| Offset | Width | Field |
| --- | --- | --- |
| 0 | 8 bytes | ASCII `SCHEMATA` |
| 8 | 1 byte | Encoding version `1` |
| 9 | 8 bytes | Original payload length |
| 17 | 2 bytes | UTF-8 filename byte length |
| 19 | 4 bytes | CRC-32/IEEE of payload only |
| 23 | variable | UTF-8 basename, including extension |
| 23 + filename length | payload length | Original bytes |

The fixed prefix is 23 bytes; the complete header is variable-length. Encode each byte high nibble first, then low nibble. For example, `0xAE` becomes purple followed by red.

| Nibble | Concrete color | Nibble | Concrete color |
| --- | --- | --- | --- |
| `0` | White | `8` | Light gray |
| `1` | Orange | `9` | Cyan |
| `2` | Magenta | `A` | Purple |
| `3` | Light blue | `B` | Blue |
| `4` | Yellow | `C` | Brown |
| `5` | Lime | `D` | Green |
| `6` | Pink | `E` | Red |
| `7` | Gray | `F` | Black |

Never derive nibble values from palette order. Every selected position must be concrete; reject selected air and other block types. Fill remaining capacity with white concrete and reject nonzero padding. Preserve empty payloads and trailing zero bytes.

A concrete unit is 16×16×16 blocks: 4,096 nibbles or 2,048 bytes. Concrete dimensions remain positive multiples of 16. Traverse complete units in y/x/z order, z fastest, and traverse each unit in the same order. Do not replace this with global z-first traversal across unit boundaries.

For normalized `(x,y,z)` and unit counts `(Ux,Uy,Uz)`:

- `unitIndex = ((y / 16) * Ux + (x / 16)) * Uz + (z / 16)`.
- `localIndex = ((y % 16) * 16 + (x % 16)) * 16 + (z % 16)`.
- `nibbleIndex = unitIndex * 4096 + localIndex`.

These rules apply to concrete headers, filenames, payloads, and padding. Keep the concrete structure self-contained when placed, copied without transformation, and re-exported. Preserve existing behavior without treating internal round trips as proof of external compatibility.

## Geometry, layout, and height

Dense and packed dimensions may be any positive integers. Remove complete-16-cubed-unit, concrete-only, and air-rejection requirements for dense and packed only. Dense represents a complete rectangular coordinate domain, including every air position; it need not be physically solid. Packed regions contain only selected full opaque cubes, so every position is solid.

Dense capacity calculations include bootstrap/header, filename, chunk rounding, and payload positions. Packed uses `requiredPositions = ceil((headerBits + compressedBits) / 10)`. Concrete uses `requiredUnits = ceil((headerBytes + payloadBytes) / 2048)`. Never apply concrete bytes-per-unit assumptions to dense or packed structures.

Retain `--max-height` as a cap, not an exact height: default 384 blocks, accepting multiples of 16 from 16 through 384, plus `0` to remove the cap. The flag's granularity does not force dense output dimensions to be multiples of 16; smaller inputs may have any positive height within the cap. Without a cap, dense height is bounded only by the 64 Mi position limit and concrete height by signed-short storage (2047 units / 32752 blocks). When the balanced cube construction would exceed those limits, select the minimum-spread rectangle within them instead of failing. Do not claim placement recovery for heights above the modern world range.

| `--layout` | Objective |
| --- | --- |
| `compact` | Minimize total positions for dense and packed or total units for concrete, then prefer balanced dimensions among equal-volume layouts. |
| `balanced` | Prioritize similar x, y, and z dimensions; allow extra padding. Default for every encoding. |
| `bounded` | Improve balance within a user-configurable padding allowance. |

Long, thin dense structures are acceptable when compact layout requires them. Balance all three axes, not only x and z. All policies must satisfy capacity, format dimension limits, resource bounds, and the height cap.

Retain `--padding-percent` as an integer from 0 through 100, default 25. For bounded dense and packed layout, allow at most `requiredPositions + floor(requiredPositions * paddingPercent / 100)` positions; for concrete, apply the same formula to required units. Never measure the allowance against payload bytes.

Document each mode's balance metric and deterministic tie-breaking before changing its layout implementation. Preserve the existing concrete layout contract. Dense and packed layout must use a pinned deterministic metric and tie-breaking, independent of workers.

Coordinates begin at local `(0,0,0)`. The modern world vertical range is −64 through 319 inclusive, so height H fits at bottom y when `−64 ≤ y ≤ 320 − H`. This geometric fact is not a dense placement-recovery guarantee. Do not impose Minecraft 1.12 height limits merely because output uses `.schematic`.

## Region boundaries and transformations

Require the exact rectangular region and its dimensions. Do not search for an encoded structure inside a larger selection. For dense data, do not infer boundaries from non-air blocks, trim outer air layers, skip air, or collapse the three air types. Added surrounding air is additional data, not harmless margin.

Support translation only. Normalize the region's minimum corner to local `(0,0,0)` before traversal. Handle signed Litematica sizes according to the pinned format: a negative size describes a selection extending negatively from its origin, not an instruction to mirror its block array.

Normalize block-entity coordinates consistently with their blocks. Reject missing, duplicate, out-of-region, or inconsistent data-bearing banner entities according to the wire specification.

Rotation and mirroring are unsupported. They can change both coordinates and encoded directional properties. Do not perform automatic orientation recovery.

## Concurrency and determinism

Use bounded Go workers for dense chunk processing, concrete unit decoding, and packed part processing. Retain the supported worker range of 1–256. Work batches are logical chunks, units, or parts, not completion-order streams; dense batches need not align to physical 16-cubed regions.

Assemble results by chunk or unit index. Single-worker and multi-worker decoding must produce identical bytes, filename parsing, and checksum results. Header and payload boundaries may cross work batches; workers must not independently guess those boundaries.

Propagate worker failures and stop outstanding work without publishing partial recovery. Bound queued work, intermediate conversion memory, and result buffering as well as worker count.

For identical payload bytes, basename, encoding version, and effective options, encoding must produce identical dimensions, coordinate states, banner layer lists, chunk boundaries, and padding regardless of scheduling or worker count. Metadata timestamps and compressed-file bytes need not be identical.

Benchmark chunk sizes and conversion before freezing the dense format. Bounded chunks enable concurrency; they do not prove maximum speed. Do not adapt wire-format chunk boundaries to runtime conditions.

Packed encode and decode process `min(workers, partCount)` parts concurrently, one zstd frame per part on one thread each. Each part reads its source range independently with `ReadAt` and hashes it in its own worker. There is no whole-file hash prepass. Packed decode writes verified parts into a destination-directory temporary file with `WriteAt` and publishes it only after every part and the aggregate digest validate. This needs free disk equal to the original length. Memory grows linearly with workers, roughly one part plus its LongArray and gzip buffers per worker (about 200 MiB per worker at the default part size, estimated). Document this relation in CLI help.

## Resource limits

Larger files use streaming multipart dense encoding under version 4, as specified in `docs/dense-v4.md`, or packed multipart under version 6. Each part is a single balanced rectangular region by default; do not lift independent per-volume limits or load the complete original file into memory. Retain older single-volume decoding. Multipart identity, offsets, counts, original length and integrity fields are structure data, not a required sidecar.

- Dense payload length is capped at 32 MiB (33,554,432 bytes) per volume, excluding header and padding. Files above this ceiling automatically use multipart with a default 16 MiB part size. Explicit `--part-size-mib` in 1..32 forces multipart. At most 4096 parts; recover the complete file only after validating all parts and the aggregate SHA-256/CRC. This is a per-volume payload ceiling, not a promise that every part fits all other limits.
- Preserve the existing concrete stream-capacity limit of 32 MiB, including header and padding. Do not silently relabel it as a payload limit.
- Packed parts hold at most 64 Mi positions × 10 bits = 80 MiB of header plus compressed or stored data. The default original part size is 64 MiB; explicit `--part-size-mib` for packed accepts 1..79 so that stored incompressible parts still fit. At most 4096 parts (about 316 GiB). This packed capacity rule replaces the 32 MiB dense payload ceiling for packed only.
- Retain at most 64 Mi block positions, 256 MiB uncompressed NBT, nesting depth 64, and 262,144 NBT nodes unless a separately approved requirement changes them.
- Apply payload, structure, NBT, node, banner-layer, chunk, and allocation limits independently. Banner entities can hit NBT limits before the payload ceiling.

Check lengths, products, additions, symbol ranks, and dimension arithmetic before allocation. Reject malformed or over-limit inputs explicitly, without panics, unchecked integer overflow, or resource growth proportional to the theoretical alphabet. Respect limits during decompression and parsing, not only after reading the whole input.

## Serialization verification and compatibility limits

Schemata's own writer and reader are the required verification target. No Litemapy, Litematica, game installation, or other external-tool run is required for dense. External re-save and world-placement compatibility remain outside the dense and packed recovery guarantees. Packed's Litematica loadability is a design requirement; report it as verified only after an actual load is exercised and recorded.

Exercise internal serialization and parsing of the complete ordinary-state catalog and targeted banner cases: zero/six layers, repeated/obscured patterns, pattern/color ordering, directional variants, and all three air types. Also exercise full encoded-file recovery through the actual CLI.

Palette reordering, compound-key reordering, compression changes, and non-payload metadata changes must not alter recovery. Verify these invariants with controlled internal fixtures. Changes to dimensions, coordinates, actual state values, or ordered banner data are not lossless for dense or packed.

Keep format specifications and authoritative Java 1.20.1 catalog provenance as implementation inputs; omitting external-tool verification does not permit inventing format behavior or narrowing the catalog.

Own writer/reader round trips prove internal recovery, not interoperability. Do not claim Litemapy or Litematica re-save compatibility without exercising it. If optional external checks are performed, record exact versions, workflows, fixture provenance, and observed results separately.

## Engineering and verification

- Use idiomatic Go with straightforward separation of CLI, versioned codecs, layout, and schematic adapters. Reuse existing patterns; avoid unnecessary abstractions.
- Preserve concrete and dense behavior while adding packed mode. Share adapter mechanics where appropriate, not concrete nibble or unit assumptions.
- Keep commits atomic: one cohesive change per commit.
- Focus permanent tests on consumer-visible behavior: byte-exact recovery, empty files, leading/trailing zeros, filename/header/chunk boundaries, arbitrary dense dimensions, all layout/height constraints, air distinctions, banner ordering, translation, indexed worker output, corruption rejection, resource boundaries, and filename/collision safety.
- Verify the alphabet mapping is reversible, including directional variants, ordinary-state entries, and banner boundary cases, without enumerating the entire combinatorial banner alphabet.
- Exercise both concrete formats, dense and packed `.litematic`, concrete and dense backward compatibility, encoding defaults/overrides, explicit dense or packed `.schematic` rejection, and deterministic logical structures across worker counts.
- Test checksum coverage over payload only, invalid padding, malformed banner entities, truncated conversions, and failure without recovered output. For packed, also test the stored fallback, the decompressed-length bound, the per-part SHA-256 and aggregate digest, mixed group identifiers, and states outside the selected palette.
- Once executable code exists for a mode, run the actual CLI through encode and decode with one and multiple workers and compare recovered bytes. Required smoke verification uses Schemata itself; external re-save runs are optional and must not be reported as verified unless exercised.
- Update user-facing docs and CLI help when implementation changes defaults or guarantees. Do not present this requirements document as evidence of completed functionality.
