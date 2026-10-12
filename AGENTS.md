# AGENTS.md

## Purpose and status

Schemata is a Go CLI that stores arbitrary files as Minecraft concrete-block structures and recovers the original bytes and filename from those structures.

This document defines required behavior, not existing functionality. The repository does not yet contain an implementation. Use explicit `encode` and `decode` commands; do not infer the operation from the input extension. A schematic file can itself be the payload.

## Supported schematic formats

Read and write both:

- Litematica `.litematic` (not `.litematica`). Reference: https://github.com/maruohon/litematica
- Legacy MCEdit `.schematic`, using its numeric block IDs and metadata for concrete. Compatibility targets modern tools that import this legacy format, not Minecraft 1.12 worlds.

Do not support Sponge `.schem` or multi-region Litematica files. Export exactly one Litematica region; reject multi-region input rather than selecting or merging regions.

Keep format adapters separate from the shared byte-to-block encoding. Both formats must represent the same logical colors, coordinates, and byte stream, regardless of palette indices or native block storage order.

Pin and document supported schematic format versions before implementing adapters. Verify against actual format specifications and external fixtures; the linked repository is not a specification for legacy `.schematic`. Reject unsupported versions explicitly.

## CLI and output files

Required interface:

- `encode`: input file, output schematic path, `--max-height`, and `--layout`. Select the output format from `.litematic` or `.schematic`; reject unsupported output extensions.
- `decode`: input schematic, destination directory, and an optional user-selected output filename. Default to the original basename stored in the header.
- Do not perform content-based file-type detection. Preserve the original extension as part of the stored filename.

Never overwrite an existing destination, for either command. On a name collision, append a numeric suffix before the final extension: `report.pdf`, `report (1).pdf`, `report (2).pdf`. Report the actual output path. A user-selected filename follows the same collision policy.

Store filenames as UTF-8 basenames only, never source directory paths. Treat header filenames as untrusted: reject names containing path separators, absolute paths, or `.`/`..` traversal names. Reject names that cannot be represented safely on the destination filesystem unless the user supplies a valid replacement filename. Never write outside the chosen destination directory.

Validate the structure, header, payload length, and checksum before publishing a recovered file. Failure must not leave a file that appears to be a successful recovery. Destination selection must remain no-overwrite even if another process creates a file concurrently.

## Byte-to-block encoding

Every block is one of the 16 concrete colors below and represents one four-bit nibble. This mapping is fixed; never derive it from a schematic's palette order.

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

Encode each byte high nibble first, then low nibble. For example, `0xAE` becomes purple followed by red.

### Units and traversal

A unit is `16×16×16` blocks: 4,096 nibbles or 2,048 bytes. Dimensions of the full structure must be positive multiples of 16.

Read complete units in order. Within each unit, z changes fastest, then x, then y. Between units, the unit's z coordinate also changes fastest, then x, then y. Equivalently, both levels use y as the outer loop, x as the middle loop, and z as the inner loop. Do not replace this with global z-first traversal across unit boundaries.

For a normalized block coordinate `(x, y, z)` and unit counts `(Ux, Uy, Uz)`:

- `unitIndex = ((y / 16) * Ux + (x / 16)) * Uz + (z / 16)` using integer division.
- `localIndex = ((y % 16) * 16 + (x % 16)) * 16 + (z % 16)`.
- `nibbleIndex = unitIndex * 4096 + localIndex`.

These rules apply to the header, filename, payload, and padding alike.

### Block-encoded header and integrity

Use one header per file, at the start of the logical byte stream. Encode it as concrete blocks, not custom schematic metadata or a sidecar. The structure must remain self-contained when placed, copied without transformation, and re-exported.

The header must contain:

- A Schemata format identifier and encoding version.
- The original payload length in bytes.
- A filename byte length and the original UTF-8 basename, including its extension.
- A CRC-32/IEEE checksum over the original payload bytes only, excluding the header and padding.

The filename makes the header variable-length. Keep it small, but do not claim a fixed total header size. Specify exact field widths, byte order, identifier, and version before implementing the codec; reject unsupported encoding versions.

The payload immediately follows the header. Fill all remaining rectangular capacity with white concrete (`0`). Padding is never air. Recover exactly the declared payload length: real zero bytes, including trailing zeros, are data and must not be trimmed. Support empty files.

Reject invalid headers, lengths that exceed available capacity, truncated payloads, and checksum mismatches. CRC32 detects accidental corruption; it is not authentication, error correction, or protection against deliberate tampering.

## Layout and height

Generate a solid rectangular volume of complete units. Include the header and filename when calculating capacity: `requiredUnits = ceil((headerBytes + payloadBytes) / 2048)`.

Expose `--max-height` as a cap, not an exact output height. Default to 64 blocks; accept multiples of 16 from 16 through 384. Smaller inputs may use less height.

Balance all three dimensions, not just x and z. Grow within the height cap, then expand horizontally when necessary. All layout policies must satisfy capacity and height constraints:

| `--layout` | Objective |
| --- | --- |
| `balanced` (default) | Prioritize similar x, y, and z dimensions; allow extra padding. |
| `compact` | Minimize total units, then prefer balanced dimensions among equal-volume layouts. |
| `bounded` | Improve balance within a user-configurable padding allowance. |

For `bounded`, measure extra units relative to `requiredUnits`, not payload bytes. Document its padding option and default, the balance metric, and deterministic tie-breaking before implementing layout selection. Layout must not depend on worker scheduling.

Use the modern-world vertical range −64 through 319 inclusive: 384 blocks. Encoded coordinates start at local `(0,0,0)`; world placement is separate. A structure of height `H` fits vertically when its bottom world coordinate satisfies `−64 ≤ y ≤ 320 − H`. Do not apply legacy Minecraft 1.12 world-height limits merely because the output uses `.schematic`.

## Decoding structure and transformations

Require a precise rectangular selection of the data structure. Do not search for a concrete volume inside a larger selection or tolerate surrounding air. Every block in the selected volume must be concrete; reject air and other block types.

Support translation only. Normalize the selected volume's minimum corner to local `(0,0,0)` before applying traversal. Do not reject an otherwise valid structure merely because its stored position differs from the encoder's origin.

Rotation and mirroring are unsupported. The original orientation and block arrangement must be preserved when placing and re-exporting. Do not attempt automatic orientation recovery.

## Concurrent decoding

Support concurrent unit decoding using bounded Go workers. Workers decode independent units; assemble their results by unit index, never by completion order. Single-worker and multi-worker decoding must produce identical payload bytes and checksum results.

The header and payload can cross unit boundaries. Concurrency must not change byte order, filename parsing, or which bytes belong to the CRC32 calculation. Propagate worker failures; do not publish partial recovery.

## Engineering and verification

- Use idiomatic Go and straightforward separation between the CLI, shared codec/layout logic, and schematic adapters. Avoid introducing abstractions without a concrete need.
- Keep commits atomic: one cohesive change per commit.
- Check lengths and dimension arithmetic before allocation; malformed input must fail without panics or unchecked resource growth.
- Keep tests focused on observable behavior: byte-exact round trips for both formats, trailing zeros and empty files, header/payload unit boundaries, height and layout constraints, translation, concurrent output ordering, corruption rejection, and filename/collision safety.
- Round trips through our own writer and reader alone do not prove compatibility. Exercise fixtures from external tools and verify generated structures with the targeted importers, including translation through placement and re-export.
- Once executable code exists, run the actual CLI through encode and decode and compare recovered bytes. Do not describe planned commands or unexercised compatibility as verified functionality.
