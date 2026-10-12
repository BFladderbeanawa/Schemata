# Schemata

Stores arbitrary file bytes and their UTF-8 basename in a solid concrete-block structure. Explicit `encode` and `decode` operations; a schematic can itself be the payload.

## Build and use

Requires Go 1.24 or newer. No runtime Go dependencies.

```sh
go build -o schemata ./cmd/schemata
schemata encode --max-height 64 --layout balanced report.pdf archive.litematic
schemata encode --layout bounded --padding-percent 25 report.pdf archive.schematic
schemata decode --workers 4 archive.litematic recovered
schemata decode --filename replacement.pdf archive.schematic recovered
```

Flags precede positional arguments. Decode creates the destination directory when absent. Both commands print the actual output path and never overwrite: collisions use `report (1).pdf`, `report (2).pdf`, etc. Diagnostics go to stderr; exit codes are 0 (success), 1 (runtime failure), and 2 (usage error).

## Pinned formats

- Litematica `.litematic`: gzip-compressed big-endian NBT, **Version 6, SubVersion 1** (also read Version 6 with absent/zero SubVersion). Write MinecraftDataVersion **3465 (Java 1.20.1)**. Exactly one region. Block state packing is continuous, least-significant bits first, with `max(2, ceil(log2(palette size)))` bits per entry, including entries crossing 64-bit words. Native order is x-fastest, then z, then y. Palette indices are not nibble values. Air may be an unused palette entry but never a selected block. Signed region dimensions describe a selection extending negatively from its origin, not a mirrored block array; normalize the minimum corner without changing block order.
- Legacy MCEdit `.schematic`: gzip-compressed big-endian NBT rooted at `Schematic`, **Materials="Alpha"**, signed-short Width/Height/Length, numeric concrete block ID **251**, metadata **0–15**. This format has no numbered version field; reject other Materials variants and explicit Version tags. Native index `(y*Length+z)*Width+x`. Optional AddBlocks must have the expected length and all high-ID bits zero for concrete. No Sponge `.schem`, Classic, or Pocket variants.
- Format references: [Litematica 1.20.1 source](https://github.com/maruohon/litematica/tree/afb3feb258aacdc52901217bec233e2bee880b21), [Litemapy format documentation](https://litemapy.readthedocs.io/en/latest/litematics.html), [legacy schematic specification](https://minecraft.wiki/w/Schematic_file_format), [WorldEdit legacy reader](https://github.com/EngineHub/WorldEdit/blob/master/worldedit-core/src/main/java/com/sk89q/worldedit/extent/clipboard/io/MCEditSchematicReader.java).

## Block stream version 1

All numeric header fields are unsigned, **big-endian**. One header begins at logical byte zero:

| Offset | Width | Field |
|---|---|---|
| 0 | 8 bytes | ASCII `SCHEMATA` |
| 8 | 1 byte | Encoding version `1` |
| 9 | 8 bytes | Original payload length |
| 17 | 2 bytes | UTF-8 filename byte length |
| 19 | 4 bytes | CRC-32/IEEE of payload only |
| 23 | variable | UTF-8 basename, including extension |
| 23 + filename length | payload length | Original bytes |

The fixed prefix is 23 bytes; the total header is variable-length. Empty payloads are supported. Remaining capacity is white concrete; trailing zero payload bytes are retained. Reject unsupported versions, malformed UTF-8/basenames, out-of-capacity lengths, checksum mismatches, and nonzero padding. CRC32 detects accidental corruption, not malicious changes.

Each byte becomes its high nibble followed by its low nibble. Nibbles 0–15 map to white, orange, magenta, light blue, yellow, lime, pink, gray, light gray, cyan, purple, blue, brown, green, red, black. Every block must be concrete.

A unit is 16×16×16 blocks (2048 bytes). Traverse complete units in y/x/z order, with z fastest, and traverse each unit in the same order. For normalized `(x,y,z)` and unit counts `(Ux,Uy,Uz)`, `unitIndex=((y/16)*Ux+x/16)*Uz+z/16`, `localIndex=((y%16)*16+x%16)*16+z%16`, `nibbleIndex=unitIndex*4096+localIndex`. Never use global z-first traversal across units.

## Layout contract

`requiredUnits=ceil((23+filenameBytes+payloadBytes)/2048)`. Dimensions are positive multiples of 16. `--max-height` defaults to 64 and accepts multiples of 16 from 16 through 384; it is a cap.

- `balanced` (default): choose the smallest integer cube side `s` with `s*s*min(s,heightCapUnits) >= requiredUnits`; use `(s,min(s,heightCapUnits),s)`. This prioritizes similar axes and permits rectangular padding.
- `compact`: minimize total units among all feasible rectangles, then minimize the balance metric below.
- `bounded`: `--padding-percent` is an integer from 0 through 100, default **25**. Permit at most `requiredUnits + floor(requiredUnits*paddingPercent/100)` units. Minimize the balance metric, then volume.

For compact/bounded, enumerate x≤z (symmetric horizontal choices), y within the height cap. Balance is first `max(x,y,z)-min(x,y,z)`, then `(x-y)^2+(y-z)^2+(z-x)^2`. Remaining ties prefer larger y, then smaller x, then smaller z. Selection is deterministic and independent of worker scheduling.

## Recovery and safety

Select exactly the rectangular volume, without surrounding air. Translation is supported, including signed Litematica region sizes; rotation and mirroring are unsupported. Preserve orientation when placing and re-exporting. A height H fits the modern world at bottom y satisfying `-64 ≤ y ≤ 320-H`; legacy file storage does not imply legacy world-height restrictions.

Decode uses bounded workers (1–256), assembling independent units by index. Validate the entire structure, header, length, checksum, and padding before writing. Names are untrusted basenames; separators, absolute paths, `.` and `..` are always rejected. Filesystem-incompatible stored names require a valid `--filename` replacement. Output publication uses an exclusively created destination; failed writes are removed, and concurrent name collisions select another suffix.

Resource limits are checked before allocation: at most **64 Mi blocks** (32 MiB stream capacity), **256 MiB uncompressed NBT**, nesting depth 64, and bounded NBT node counts. Inputs above these limits fail explicitly. Adapters do not inspect payload content or infer the operation from an extension.

## Verification

```sh
go test ./...
go test -race ./...
```

Own writer/reader round trips do not prove importer compatibility. Compatibility results and any unavailable in-game placement checks are recorded below when exercised.

## License

See [LICENSE](LICENSE).
