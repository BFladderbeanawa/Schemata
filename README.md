# Schemata

Stores arbitrary file bytes and their UTF-8 basename in Minecraft schematic structures. Explicit `encode` and `decode` operations; a schematic can itself be the payload.

## Build and use

Requires Go 1.24 or newer. No runtime Go dependencies.
Windows PowerShell:

```powershell
go build -o schemata.exe ./cmd/schemata
.\schemata.exe encode report.pdf archive.litematic
.\schemata.exe decode --workers 4 archive.litematic recovered
```

PowerShell requires `.\` for executables in the current directory unless their directory is on PATH. Build with the `.exe` suffix on Windows. The supported Litematica extension is `.litematic`, not `.litematica`.

Linux/macOS:


```sh
go build -o schemata ./cmd/schemata
./schemata encode report.pdf archive.litematic
./schemata encode --banners report.pdf banner-dense.litematic
./schemata encode --encoding concrete --layout balanced report.pdf concrete.litematic
./schemata encode --layout bounded --padding-percent 25 report.pdf archive.schematic
./schemata decode --workers 4 archive.litematic recovered
./schemata decode --filename replacement.pdf archive.schematic recovered
```

Flags precede positional arguments. Decode creates the destination directory when absent. Both commands print the actual output path and never overwrite: collisions use `report (1).pdf`, `report (2).pdf`, etc. Diagnostics go to stderr; exit codes are 0 (success), 1 (runtime failure), and 2 (usage error).

### Large files: streaming multipart

Dense inputs above 32 MiB automatically use multipart output, with 16 MiB payload per volume. Each volume is one balanced rectangular region; no flags are needed for the large-file path. For requested `1.litematic`, output is a collision-free `1.parts` directory containing numbered `.litematic` files. The command prints that directory, not a nonexistent single file. Existing directories receive numeric suffixes.

```powershell
.\schemata.exe encode large.zip 1.litematic
.\schemata.exe decode --workers 4 1.parts recovered
# Force multipart or choose part size (1–32 MiB):
.\schemata.exe encode --part-size-mib 8 large.zip archive.litematic
```

Decode also accepts any part file when all its siblings are together. Parts may be renamed or reordered; their identity and order are stored inside the structures. Mixed sets, duplicate/missing parts, bad per-part CRC/padding, and aggregate SHA-256/CRC failures prevent publication of a recovered file. `--filename` and numeric collision suffixes apply as usual. Multipart is dense-only; concrete retains its single-volume capacity limit.

Encoding hashes the source in a streaming prepass, then reads/encodes/writes one part at a time and checks for source changes. Decode validates parts one at a time into a temporary disk file, verifies the whole file, then publishes through exclusive no-overwrite creation. RAM is bounded by a single part's structures and worker buffers, not total original length; temporary disk must hold the full recovered file. At most 4096 volumes (up to 128 GiB at 32 MiB per part, subject to each volume's independent limits). Keep parts in a dedicated directory. [Version 4 wire specification](docs/dense-v4.md).


## Encoding modes

| Mode | Output formats | Default layout | Recovery guarantee |
|---|---|---|---|
| `dense` (writes version 3 single-volume / 4 multipart; reads 2–4) | `.litematic` | `balanced` | Byte-exact through Schemata's own writer/reader only |
| `concrete` (version 1) | `.litematic`, `.schematic` | `balanced` | Existing concrete encoding; translation without rotation or mirroring |

With no `--encoding`, `.litematic` selects dense and `.schematic` selects concrete. Explicit `--encoding dense` with `.schematic` is rejected, never downgraded. Explicit `--encoding concrete` retains concrete `.litematic` output. Decode detects the in-structure identifier/version; no encoding flag is needed. An explicit layout overrides the mode's default.

Dense encoding defaults to the **23,815 nonbanner vanilla Java 1.20.1 complete block states**, including directional/transient states and all three distinct air types. No banner blocks or banner tile entities are emitted. Use `encode --banners` to enable the full alphabet: all **24,135 registered states** plus native banner base colors, properties, and ordered zero-through-six-layer pattern/color lists. All **41 native pattern hashes** accepted by the pinned game parser are included, including explicit base pattern `b`; repeated or obscured layers are data. The full combinatorial alphabet has **25,540,862,200,767,107,655 symbols**, ranked without materializing combinations. `--banners=false` explicitly disables banners; `--banners` is rejected for concrete encoding. Decode needs no banner flag and still reads existing banner-encoded files. [Catalog provenance and reproducible independent validation](internal/alphabet/PROVENANCE.md).

Dense version 3 has one fixed 46-position concrete bootstrap followed by independently decodable 256-byte chunks. By default every chunk uses the nonbanner alphabet. With `--banners`, chunks use the full banner/state alphabet while the banner NBT budget permits, then fall back within dense mode to nonbanner states. Each chunk records its radix choice in an ordinary-state marker; it never silently switches to concrete or raises resource limits. Disabling banners requires more block positions, not necessarily a larger compressed file. Header lengths preserve empty payloads and leading/trailing zeros; CRC-32/IEEE covers only payload bytes. Arbitrary positive dimensions include every air position. Padding is canonical rank zero (`acacia_button` with pinned properties), not white concrete or an end marker. [Version 3 fallback specification](docs/dense-v3.md); [version 2 wire specification and conversion measurements](docs/dense-wire.md). Existing version 2 files remain readable.

**Dense is schematic-only.** No recovery guarantee after placement, ticking, chunk reload, world export, or external-tool re-saving. Unsupported or unstable world states remain eligible symbols. Larger palettes and banner NBT may increase compressed file size; the objective is more bytes per block position, not smaller files. Only native banner patterns carry additional data; arbitrary inventories, text, or custom NBT do not.

## Pinned formats

- Litematica `.litematic`: gzip-compressed big-endian NBT, **Version 6, SubVersion 1** (also read Version 6 with absent/zero SubVersion). Write MinecraftDataVersion **3465 (Java 1.20.1)**. Exactly one region. Block state packing is continuous, least-significant bits first, with `max(2, ceil(log2(palette size)))` bits per entry, including entries crossing 64-bit words. Native order is x-fastest, then z, then y. Palette indices are not encoding symbols. Concrete rejects selected air; dense includes every air position and preserves all three air types. Signed region dimensions describe a selection extending negatively from its origin, not a mirrored block array; normalize the minimum corner without changing block order. Banner tile coordinates remain minimum-corner-local, independently of translated Position or signed Size.
- Legacy MCEdit `.schematic`: gzip-compressed big-endian NBT rooted at `Schematic`, **Materials="Alpha"**, signed-short Width/Height/Length, numeric concrete block ID **251**, metadata **0–15**. This format has no numbered version field; reject other Materials variants and explicit Version tags. Native index `(y*Length+z)*Width+x`. Optional AddBlocks must have the expected length and all high-ID bits zero for concrete. No Sponge `.schem`, Classic, or Pocket variants.
- Format references: [Litematica 1.20.1 source](https://github.com/maruohon/litematica/tree/afb3feb258aacdc52901217bec233e2bee880b21), [Litemapy format documentation](https://litemapy.readthedocs.io/en/latest/litematics.html), [legacy schematic specification](https://minecraft.wiki/w/Schematic_file_format), [WorldEdit legacy reader](https://github.com/EngineHub/WorldEdit/blob/master/worldedit-core/src/main/java/com/sk89q/worldedit/extent/clipboard/io/MCEditSchematicReader.java).

## Concrete block stream version 1

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

Concrete: `requiredUnits=ceil((23+filenameBytes+payloadBytes)/2048)`. Dimensions are positive multiples of 16. `--max-height` defaults to **384** and accepts multiples of 16 from 16 through 384; it is a cap, not an exact height. `--max-height 0` removes the cap: dense layouts are then bounded only by the 64 Mi position limit, and concrete layouts by the signed-short storage maximum of 32752 blocks (2047 units). An uncapped height can exceed the modern world's 384-block vertical range; that is a layout choice with no placement guarantee.

- `balanced` (concrete default): choose the smallest integer cube side `s` with `s*s*min(s,heightCapUnits) >= requiredUnits`; use `(s,min(s,heightCapUnits),s)`. This prioritizes similar axes and permits rectangular padding. When that product would exceed the resource limit, balanced selects the minimum-spread rectangle within the limit instead, so a representable capacity is never rejected.
- `compact`: minimize total units among all feasible rectangles, then minimize the balance metric below.
- `bounded`: `--padding-percent` is an integer from 0 through 100, default **25**. Permit at most `requiredUnits + floor(requiredUnits*paddingPercent/100)` units. Minimize the balance metric, then volume.

For compact/bounded, enumerate x≤z (symmetric horizontal choices), y within the height cap. Balance is first `max(x,y,z)-min(x,y,z)`, then `(x-y)^2+(y-z)^2+(z-x)^2`. Remaining ties prefer larger y, then smaller x, then smaller z. Selection is deterministic and independent of worker scheduling. Without a cap, the height bound becomes the volume limit, so compact/bounded enumeration cost grows with the required capacity.

Dense uses the same balance metric and deterministic ties in **block positions**, with arbitrary positive dimensions under the same height cap (default 384 blocks, `--max-height 0` for no cap). `balanced` is the dense default: choose the smallest sufficient block-based cube with capped y, allowing padding to avoid long, thin exact-factorization layouts; without a cap the result is a true cube whenever one fits the 64 Mi position limit. Explicit `compact` minimizes total positions before balance and may produce thin or very long rectangles. `bounded` permits at most `requiredPositions + floor(requiredPositions*paddingPercent/100)` positions. Required capacity includes the bootstrap, filename, payload, and fixed chunk rounding. Every layout writes one rectangular region; 256-byte codec chunks are logical conversion batches, not separate physical regions. [Dense layout contract](docs/dense-la…

## Recovery and safety

Select exactly the rectangular volume, without surrounding air. Translation is supported, including signed Litematica region sizes; rotation and mirroring are unsupported. Concrete retains placement-oriented requirements; dense makes no placement-recovery guarantee. Never trim or skip dense outer air. A height H fits the modern world at bottom y satisfying `-64 ≤ y ≤ 320-H`; this geometry is not a dense recovery guarantee, and legacy file storage does not imply legacy world-height restrictions.

Decode uses bounded workers (1–256), assembling independent concrete units or dense chunks by index. Validate the entire structure, header, length, checksum, and padding before writing. Names are untrusted basenames; separators, absolute paths, `.` and `..` are always rejected, including stored traversal names when a replacement is requested. Filesystem-incompatible stored names require a valid `--filename` replacement. Output publication uses an exclusively created destination; failed writes are removed, and concurrent name collisions select another suffix.

Independent per-volume resource limits: dense **32 MiB payload** excluding header/padding; concrete **32 MiB stream capacity** including header/padding; **64 Mi positions**, **256 MiB uncompressed NBT**, nesting depth 64, and **262,144 NBT nodes**. Dense multipart retains these limits for every part and bounds the set to 4096 parts. Dense defaults to nonbanner chunks; with `--banners`, it reserves palette/scaffolding nodes and bounds banner use, converting remaining chunks with ordinary nonbanner states. Lower-density nonbanner chunks use more positions; the payload ceiling still does not guarantee every layout fits independent limits. Lengths, dimensions, ranks, layer counts, and NBT budgets are checked before relevant allocations/publication. Adapters do not inspect payload content or infer the operation from an extension.

## Verification

```sh
go test ./...
go test -tags=integration ./...
go test -race -tags=integration ./...
```

Verified for the dense expansion on Windows with Go 1.24.1:

- Integration and race-enabled integration suites passed, including all ordinary catalog states through serialization, banner ordering/boundaries, distinct air states, arbitrary dense dimensions, layout/resource constraints, indexed worker recovery, malformed data, and existing concrete fixtures.
- Actual built CLI: dense `.litematic`, concrete `.litematic`, and concrete `.schematic` × all three layouts × one/four workers recovered a 4,356-byte UTF-8-named payload byte-exactly, including trailing zeros. Also exercised empty dense files, schematic-as-payload, defaults, explicit dense legacy rejection, and encode/decode collisions.
- Controlled dense CLI fixtures recovered byte-exactly after palette/key reordering, compression changes, nonpayload metadata changes, translation, and signed sizes. CRC corruption and a missing banner entity failed without creating a destination.
- Dense version 3 actual CLI encoded the previously failing 1,628,423-byte document and recovered it byte-exactly with 1 and 4 workers, without changing NBT node limits. Regression coverage preserves version 2 recovery and rejects invalid chunk markers.
- Nonbanner-default actual CLI encoded the 1,628,423-byte document with zero banner palette states and zero banner entities, recovering identical bytes with 1 and 4 workers. Also exercised default, `--banners`, `--banners=false`, and concrete `.litematic` recovery on a 1,025-byte payload containing leading/trailing zeros; integration tests passed.
- Balanced-default actual CLI encoded the same 1,628,423-byte document as one 119×64×119 region (906,304 positions), replacing compact's 1×5×180,667 shape (903,335 positions) with 0.329% additional padding. Recovered byte-exactly with 1 and 4 workers. Explicit compact/bounded and concrete balanced output also recovered; the full integration suite passed.
- Version 4 actual CLI encoded the 1,995,772,310-byte ZIP into 119 volumes at the default 16 MiB part size. First/last volumes were single 382×64×382 / 374×64×374 regions without banner entities. Full recovery with 1 and 4 workers matched SHA-256 `28f66a30ecbb8de1aa4f22e7f1c32c8326e94c508e5e7026af49747efd7983d5`. Integration and race-enabled suites passed, including renamed/reordered parts, missing/duplicate/mixed/corrupt sets, aggregate-integrity failure, collision safety, empty input and cancellation.
- Catalog acquisition independently matched official generated reports against the live Mojang registry; native banner parsing exercised all 41 hashes × 16 colors × lengths 0–6. See [catalog provenance](internal/alphabet/PROVENANCE.md).

Historical **concrete-only** external verification (not rerun for this expansion): Litemapy 0.11.0b0 import/translated re-export and WorldEdit 7.2.20 legacy import. Independently generated concrete fixtures and exact provenance remain in [testdata/PROVENANCE.md](internal/schematic/testdata/PROVENANCE.md). These results do not establish dense interoperability.

**Not verified or guaranteed for dense:** external-tool re-save, in-game import, world placement, ticking/reload, or world-to-schematic recovery. Own writer/reader round trips prove internal recovery only. Concrete in-game placement-to-re-export also remains unverified.

## Changes

- Added the dependency-free encode/decode CLI, encoding version 1, deterministic layouts, indexed concurrent recovery, pinned schematic adapters, and no-overwrite output handling.
- Added dense encoding version 2 with the complete pinned Java 1.20.1 alphabet, compact banner ranking, fixed bounded chunks, banner-aware Litematica serialization, arbitrary dense dimensions, mode-specific defaults, and automatic encoding dispatch. Preserved concrete version 1 in both formats.
- Added dense version 3 resource-aware full-alphabet/nonbanner chunk fallback to balance banner NBT cost with ordinary-state density. Retained version 2 and concrete version 1 decoding.
- Changed dense encoding to default to nonbanner states. Added `encode --banners` to opt into the full alphabet and resource-aware fallback, without changing version 3 markers or earlier-file decoding.
- Changed dense's default layout from compact to balanced for three-dimensional rectangular output. Explicit compact and bounded policies, height/resource limits, logical chunk boundaries, and existing-file decoding are unchanged.
- Added dense version 4 streaming multipart output and automatic whole-file recovery, preserving per-volume safety limits and versions 1–3 decoding. Large dense inputs no longer fail solely at the single-volume 32 MiB payload ceiling.
- Raised the `--max-height` default from 64 to **384** blocks (the full modern world height) and made `--max-height 0` remove the cap for taller, cube-like layouts. Balanced layouts whose cube product would exceed the resource limit now fall back to the minimum-spread rectangle inside the limit instead of failing, and uncapped concrete heights stay within signed-short storage.

## License

See [LICENSE](LICENSE).
