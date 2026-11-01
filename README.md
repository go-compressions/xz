# go-compressions/xz

[![CI](https://github.com/go-compressions/xz/actions/workflows/ci.yml/badge.svg)](https://github.com/go-compressions/xz/actions/workflows/ci.yml)
![coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)

A pure-Go (`CGO_ENABLED=0`) **decoder** for `.xz`, raw **LZMA2** and `.lzma`
("LZMA alone") whose memory follows the decoded data, not the sizes a header
declares.

```go
import "github.com/go-compressions/xz"

r, err := xz.NewReader(file)          // one or several concatenated streams
if err != nil { /* ... */ }
_, err = io.Copy(dst, r)              // streaming: output is never buffered whole
```

It replaces the decode side of
[`github.com/ulikunitz/xz`](https://github.com/ulikunitz/xz) with the same
shape of API (`xz.NewReader`, `xz.ReaderConfig`, `lzma.NewReader`,
`lzma.NewReader2`), so a consumer swaps the import path.

## Why

`ulikunitz/xz` v0.5.17 allocates every LZMA2 dictionary whole, from the block
header: `lzmafilter.go:75-77` takes `max(ReaderConfig.DictCap, header)` (so
`DictCap` is a *minimum*) and `lzma/buffer.go:23` does `make([]byte, size+1)`.
A header of about a hundred bytes can ask for up to 4 GiB, and no option
bounds it. Measured with `go run ./internal/bombdemo` (44 bytes of data in one
block, the header declaring a large dictionary):

| declared dictionary | this library | ulikunitz/xz v0.5.17 |
|---|---|---|
| 1 GiB | 22 672 bytes allocated | 1 073 793 080 bytes |
| 1.5 GiB | 22 672 bytes allocated | 1 610 658 656 bytes |
| 4 GiB - 1 | 22 672 bytes allocated | not run (allocates 4 GiB by the code above) |

## Design

### Bibliography (opened before any code was written)

| Source | Used for | Licence |
|---|---|---|
| [`xz-file-format.txt`](https://tukaani.org/xz/xz-file-format.txt) (Lasse Collin) | the container: stream header/footer, block header, index, padding, checks, filter IDs, VLI, Delta, BCJ properties | public domain, may also be distributed under 0BSD (XZ Utils `COPYING`) |
| [`doc/lzma-file-format.txt`](https://github.com/tukaani-project/xz/blob/master/doc/lzma-file-format.txt) | the `.lzma` header | public domain / 0BSD (same) |
| LZMA SDK [`DOC/lzma-specification.txt`](https://github.com/jljusten/LZMA-SDK/blob/master/DOC/lzma-specification.txt) and its reference decoder [`LzmaSpec.cpp`](https://github.com/jljusten/LZMA-SDK/blob/master/CPP/7zip/Bundles/LzmaSpec/LzmaSpec.cpp) (Igor Pavlov, 2015-06-14) | the LZMA decoder: ported exactly, it is the equality oracle | public domain (header of the file) |
| XZ Utils [`liblzma/lzma/lzma2_decoder.c`](https://github.com/tukaani-project/xz/blob/master/src/liblzma/lzma/lzma2_decoder.c) | LZMA2 chunk control bytes: dictionary / state / properties resets, uncompressed chunks, chunk size checks | 0BSD (SPDX header) |
| XZ Utils `liblzma/simple/*.c`, `liblzma/delta/delta_decoder.c` | the Delta and BCJ filters (x86, PowerPC, IA-64, ARM, ARM Thumb, SPARC, ARM64, RISC-V) | 0BSD |
| XZ Utils [`tests/files`](https://github.com/tukaani-project/xz/tree/master/tests/files) and its README | 80 conformance files (25 good, 50 bad, 5 unsupported), embedded under `internal/testfiles/data/xz-utils` | 0BSD (README of that directory) |
| Linux `lib/xz/xz_dec_bcj.c` (0BSD) and `fs/squashfs/xz_wrapper.c` (GPL-2.0-or-later, **read only, nothing copied**) | which filters squashfs images can use (below) | — |

All of it is public domain or 0BSD except the squashfs wrapper, which was only
read for the list of filters. The repository is BSD-3-Clause.

### The lazy window

The reference decoder (`COutWindow::Create(dictSize)`) allocates the whole
dictionary up front. That is the one place this port departs from it:

* the window starts at 16 KiB (or the dictionary size if smaller) and **doubles
  as `Pos` advances**, up to the dictionary size; it wraps only once it has
  reached it;
* growth happens only when the write position reaches the end of the buffer,
  so there is no per-byte check in the hot loop;
* a growth that would pass `MaxDictionary` fails with `ErrTooLarge` **before**
  anything is allocated, and the bytes decoded up to that point have already
  been delivered.

Why the output is unchanged: the reference's own check is
`CheckDistance(dist) = dist <= Pos || IsFull`. While the window is not full,
every distance the reference accepts points into bytes already written, at
`Pos - dist`; a buffer that holds exactly `[0, Pos)` contains all of them and
returns the same bytes. Once `Pos` reaches the dictionary size the window is
the dictionary size, wraps as in the reference, and from then on the two are
identical. A header declaring a 4 GiB dictionary over a few bytes of data
therefore allocates kilobytes. `TestRandomScripts` (symbol-level streams, dictionaries
4 KiB to 1 MiB, wrapping several times) and the 36 + 80 fixture files check this.

A second, smaller departure: the reference's `CheckDistance(rep0)` takes
`rep0 <= Pos`, which for `rep0 == Pos` reads one byte before the data
(`Buf[Size-1]`, uninitialised in the reference when the window is not full).
`LzmaDec.c` and liblzma refuse `rep0 >= Pos`; so does this decoder. (The
xz-utils test file `bad-1-lzma2-2.xz` exercises this.)

Everything else follows the reference: range decoder, probability tables,
literal / match / rep / short-rep coding, end marker, and the three
`unpackSize` modes (unknown size with mandatory marker, known size with an
optional marker, known size without marker, the LZMA2 chunk case).

Other allocations follow the data too: the literal probability tables of a
`.lzma` header with `lc+lp` up to 12 (6 MiB if fully used) grow with the highest
literal context actually seen; an LZMA2 chunk is read in 16 KiB steps whatever
its header claims (the format caps it at 64 KiB); block sizes and Index counts
are validated against what arrives and never used to size a buffer (the Index is
checked with a running SHA-256 of the records, in constant memory).

### Bounds

`ReaderConfig{MaxDictionary int; SingleStream bool}`. A zero `MaxDictionary` is
`DefaultMaxDictionary`, **256 MiB** (above the 64 MiB of `xz -9`). The ceiling is
exact: data that fits in it decodes, whatever dictionary the header declares;
one byte more is `ErrTooLarge` (`TestCeilingExactness`, byte for byte, both for
`.xz` and `.lzma`). `DictCap` is accepted and ignored so that code written for
`ulikunitz/xz` compiles; it was a *minimum* there.

Errors are typed and work with `errors.Is`: `ErrCorrupt` (every malformed
input), `ErrChecksum` and `ErrTruncated` (both are also `ErrCorrupt`;
`ErrTruncated` is also `io.ErrUnexpectedEOF`), `ErrTooLarge`, `ErrUnsupported`.
Nothing panics on input; a failure is sticky. Data of a block is delivered
before its check is verified; a failed check follows the data.

### Container

Stream header/footer with CRC32, block headers (up to 4 filters, CRC32,
padding), Index validation (record count, unpadded and uncompressed sizes,
padding, CRC32, Backward Size), Stream Padding and concatenated streams (or
`SingleStream`), checks None / CRC32 / CRC64 / SHA-256 (verified). Reserved check
types, unknown filter IDs, reserved flag bits and a filter chain the format
does not allow are `ErrUnsupported`, as liblzma reports them.

### Filters

| ID | Filter | Status |
|---|---|---|
| `0x21` | LZMA2 | yes |
| `0x03` | Delta | yes |
| `0x04` | BCJ x86 | yes |
| `0x05` | BCJ PowerPC | yes |
| `0x06` | BCJ IA-64 | yes |
| `0x07` | BCJ ARM | yes |
| `0x08` | BCJ ARM Thumb | yes |
| `0x09` | BCJ SPARC | yes |
| `0x0A` | BCJ ARM64 | yes (with start offsets) |
| `0x0B` | BCJ RISC-V | yes |

All eight BCJ filters and Delta are checked against fixtures written by the
`xz` command line tool (`go run ./internal/mkfixtures`), including chains of
three and four filters, start offsets, and the filter test files of XZ Utils.

**squashfs.** The kernel decodes squashfs XZ images with its XZ Embedded copy
(`fs/squashfs/xz_wrapper.c` calling `lib/xz`), whose BCJ filters in
`lib/xz/xz_dec_bcj.c` are x86, PowerPC, ARM, ARM Thumb, SPARC, ARM64 and RISC-V
(IA-64 was dropped with the architecture); `mksquashfs -Xbcj` writes x86, arm,
armthumb, powerpc, sparc and ia64. This decoder covers every one of them, so any
squashfs XZ image decodes, whichever filter its blocks use.

## API

```go
package xz
func NewReader(r io.Reader) (*Reader, error)
func (c ReaderConfig) NewReader(r io.Reader) (*Reader, error)
func (c *ReaderConfig) Verify() error
type ReaderConfig struct { MaxDictionary int; DictCap int /* ignored */; SingleStream bool }
const DefaultMaxDictionary = 256 << 20
var ErrCorrupt, ErrChecksum, ErrTruncated, ErrTooLarge, ErrUnsupported error

package lzma // .lzma and raw LZMA2 (7z, zip method 14)
func NewReader(r io.Reader) (*Reader, error)                    // .lzma
func (c ReaderConfig) NewReader(r io.Reader) (*Reader, error)
func (r *Reader) Header() Header
func NewReader2(r io.Reader) (*Reader2, error)                  // raw LZMA2
func (c Reader2Config) NewReader2(r io.Reader) (*Reader2, error)
type Reader2Config struct { DictCap int; MaxDictionary int }    // DictCap: the container's dictionary size
func DictCapFromProp(prop byte) (uint32, error)                  // the 7z / xz property byte
func (c Reader1Config) NewReader1(r io.Reader) (*Reader1, error) // headerless LZMA1 with end marker (lzip)
type Reader1Config struct { LC, LP, PB uint; DictCap, MaxDictionary int }
func (r *Reader1) Unconsumed() []byte                            // read-ahead left over after io.EOF
```

`Reader1` decodes the headerless LZMA1 coding inside an lzip member (lc, lp, pb
and the dictionary come from the container, the size is unknown, an end marker
ends the stream). The decoder reads ahead from its source, so the bytes that
follow the stream (the lzip trailer, the next member) are returned by
`Unconsumed` once `Read` has reported `io.EOF`; the caller pushes them back.

Both packages re-export the same error variables. All readers are streaming
`io.Reader`s. The `.xz` reader reads from the source exactly the bytes it uses
(no read-ahead); the `.lzma` reader buffers 32 KiB.

## Tests

* **Fixtures from the C reference** (`xz` 5.8.4, `internal/mkfixtures`): 36 files
  (all checks, presets 0 and 9e, 4 KiB dictionary so the window wraps, lc/lp/pb
  variants, multi-block, stored chunks, empty, all filters, `.lzma`).
* **XZ Utils test files** (`tests/files`): every good file decodes (and equals
  `xz -dc` where `xz` is installed), every bad file is refused with a typed error,
  every unsupported file is `ErrUnsupported`.
* **Differential** against `ulikunitz/xz`'s own writer (10 configurations ×
  4 kinds of data: checks, block sizes, properties, match algorithms) and, as
  decoder oracle, against its reader.
* **Symbol-level LZMA encoder** (`internal/lzmacore/enc_test.go`) reaches every
  path of the decoder, valid or not: all `unpackSize` modes, every corruption
  branch, the end marker with trailing state, repeat/short-repeat in an empty
  window, distances beyond the data or the dictionary, matches crossing the
  declared size, and LZMA2 chunk sequences with every reset level.
* **Bombs** (`TestBomb*`, run in CI as compiled binaries under `ulimit -v` and
  `GOMEMLIMIT`): headers declaring 1, 1.5, 2, 3 and 4 GiB - 1 over tiny data
  (stored and LZMA-compressed) allocate < 1 MiB; ceilings refuse before
  allocating; block sizes and index counts of 2^62 are refused; a chunk that
  claims 64 KiB and delivers 10 bytes allocates nothing near 64 KiB.
* **Truncation and bit flips**: every proper prefix of a stream is an
  `ErrCorrupt`; flipping any byte never panics and never yields wrong data
  without an error under SHA-256.
* **Fuzzing**: `FuzzXZ`, `FuzzLZMA`, `FuzzLZMA2` (differential against
  `ulikunitz/xz` wherever both decode, output bounded, errors must be typed),
  seeds from every fixture; see the results below. Crashers are committed under
  `testdata/fuzz/` **and** written out as named unit tests, because the emulated CI
  lanes run away from `testdata/`.
* **100 % statement coverage**, gated in CI across the library packages, on
  amd64, arm64, windows and — through cross-compiled test binaries under QEMU —
  riscv64, ppc64le, s390x (big-endian) and loong64; plus 386 for 32-bit.

### Fuzzing results

100 s per target, 3 workers, on an Apple silicon laptop (`GOMEMLIMIT=3GiB`):

| target | executions | corpus after the run | crashers |
|---|---|---|---|
| `FuzzXZ` | 6.83 M | 232 | 0 |
| `FuzzLZMA` | 3.76 M | 306 | 0 |
| `FuzzLZMA2` | 5.96 M | 168 | 0 |

The only disagreements the seeds produced are on the *oracle's* side:
`ulikunitz/xz` v0.5.17 accepts at least two files that XZ Utils lists as `bad-`
(one with a non-minimal variable-length integer, one with an LZMA2 chunk whose
data exceeds its declared size) and this decoder refuses them, so the
differential only demands equal bytes where both decoders succeed. A weekly
workflow (`fuzz.yml`) keeps fuzzing; a crasher it finds is committed under
`testdata/fuzz/` and written out as a named unit test.

## Performance

`go run ./internal/benchxz -work <durable dir>` decodes a 64 MiB corpus (the Go
source tree and toolchain binaries of the installed Go, compressed once with
`xz -6 -T1`) with this library, with `ulikunitz/xz` v0.5.17 and with `xz -dc`
(C), single core, in interleaved rounds, and prints MB/s and ratios. It refuses
to run when the 1-minute load average is above 8, because the figure would
measure the machine.

**Measurement deferred.** The 1-minute load average of the development machine
was 32 to 47 throughout this work (about 25 sessions share it), so the tool
refused to measure (exit status 3) and no throughput figure is claimed here. The
decode loop is the reference's straight port with no per-byte check besides the
range decoder's input refill; optimisation, algorithm first, waits for the table.

## Layout

```text
.                      package xz: container, checks, filter chain
lzma/                  package lzma: .lzma and raw LZMA2
internal/lzmacore/     LZMA decoder (port of LzmaSpec.cpp), lazy window, LZMA2 chunks
internal/filter/       Delta and BCJ filters
internal/xerr/         shared error values
internal/testfiles/    embedded fixtures (xz CLI + XZ Utils tests/files)
internal/mkfixtures/   regenerates the fixtures with the xz command line tool
internal/bombdemo/     the table above
internal/benchxz/      the benchmark below
```

## Licence

BSD-3-Clause. The code ported from the LZMA SDK reference decoder (public
domain) and from XZ Utils (0BSD) is credited in the sources.
