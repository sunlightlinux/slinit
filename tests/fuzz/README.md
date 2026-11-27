# Fuzz Tests

Go native fuzz tests (`testing.F`) for slinit's input parsing surfaces.

## This directory is not all of them

29 targets live here. Another 23 sit beside the code they exercise,
because a fuzz target for an unexported parser has to live in its own
package:

| Package | Targets |
|---------|---------|
| `pkg/service` | `FuzzStateMachine` |
| `cmd/slinit-runit-convert` | `FuzzAnalyzeRunScript`, `FuzzParseChpst` |
| `cmd/slinit-openrc-convert` | `FuzzParseOpenrcScript`, `FuzzParseDepend` |
| `pkg/config` | `FuzzParseSystemdUnit` |
| `cmd/slinit-sysvinit-convert` | `FuzzParseInittab` |
| `cmd/slinit-killall5` | `FuzzParseStat` |
| `cmd/slinit-sysusers` | `FuzzSysusersParseLine` |
| `cmd/slinit-tmpfiles` | `FuzzTmpfilesParseLine` |
| `cmd/slinit-immortal-convert` | `FuzzParseImmortalYAML` |
| `pkg/timedate` | `FuzzReadZoneTab`, `FuzzValidateZone` |
| `pkg/hostname` | `FuzzDecodeValue`, `FuzzLoadMachineInfo`, `FuzzParseOSRelease` |
| `pkg/journal` | `FuzzParseSyslog`, `FuzzAppendJSONMatchesMarshal` |
| `pkg/process` | `FuzzMatchFDName` |
| `pkg/control` | `FuzzControlServer`, `FuzzParseShutdownTime` |
| `cmd/slinit` | `FuzzScanSysArgs` |
| `cmd/slinit-journalctl` | `FuzzRenderSenderFields` |

So `go test -fuzz=... ./tests/fuzz/` reaches about three fifths of the
fuzzing surface. CI fuzzes every package in its matrix
(`.github/workflows/tests.yml`), and `go test ./tools/stats` fails when
a package holding a target is missing from it. The authoritative list is:

```bash
grep -rl '^func Fuzz' --include='*_test.go' .
```

## Running

```bash
# Run a specific fuzz target for 30 seconds
go test -fuzz=FuzzConfigParse -fuzztime=30s ./tests/fuzz/

# Run all fuzz targets briefly (seed corpus only)
go test ./tests/fuzz/

# Run with extended time for deeper coverage
go test -fuzz=FuzzReadPacket -fuzztime=5m ./tests/fuzz/

# Run all fuzz targets sequentially. Anchor the regex with ^$ so a name
# that's a prefix of another target (e.g. FuzzDecodeServiceStatus vs
# FuzzDecodeServiceStatus5) doesn't fail with "matches more than one".
for f in $(go test -list 'Fuzz.*' ./tests/fuzz/ 2>/dev/null | grep ^Fuzz); do
    echo "=== $f ==="
    go test -fuzz="^${f}$" -fuzztime=30s ./tests/fuzz/
done
```

## Invariants

Every target enforces at least the "must not panic" invariant. Where
the underlying API is a pure `(bytes ⇄ struct)` codec pair, the fuzz
also enforces:

- **Round-trip fidelity** — `decode(encode(decode(bytes))) == decode(bytes)`.
  A decoder that tolerates bytes the encoder never emits (or vice
  versa) creates a wire drift between daemon and client versions;
  the round-trip check catches such drift on the first fuzz
  iteration that hits it.
- **Semantic wire-vs-loader consistency** — bytes that decode
  cleanly at the wire layer must not encode names the on-disk
  loader would reject (`ValidateServiceName`). Closes defense-in-
  depth gaps like "wire accepts NUL bytes in service names but
  filesystem paths would truncate at NUL."

## Targets

### Config Parsing (config_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzConfigParse | Main service config file parser (text grammar). Seed corpus includes every real config from `demo/services/` (54 files as of v2.2.10) so the mutator starts from production-shaped inputs. Post-parse the desc's slice/map fields are traversed to catch partial-initialisation nil-deref shapes. |
| FuzzParseIDMapping | Namespace UID/GID mapping "container:host:size" |
| FuzzParseCPUAffinity | CPU affinity spec "0-3 8-11" |
| FuzzParseLSBHeaders | /etc/init.d LSB header block parser |

### Control Protocol (protocol_fuzz_test.go)
| Target | What it fuzzes | Round-trip? |
|--------|----------------|:-----------:|
| FuzzReadPacket | Binary packet reader [type(1)+len(2)+payload(N)] | — |
| FuzzDecodeServiceName | Service name [len(2)+name(N)] | ✓ |
| FuzzDecodeHandle | uint32 handle | ✓ |
| FuzzDecodeMetadata | Author/version/usage triplet | ✓ |
| FuzzDecodeServiceStatus | 12-byte service status | (encoder needs live svc) |
| FuzzDecodeServiceStatus5 | 14-byte v5 service status | (encoder needs live svc) |
| FuzzDecodeSetEnv | Set-env request (handle + KEY=VALUE) | ✓ |
| FuzzDecodeEnvList | Env list reply (KEY=VALUE\0 pairs) | ✓ (via map value-equality) |
| FuzzDecodeDepRequest | Add/remove dependency request | ✓ |
| FuzzDecodeBootTime | Boot timing info | ✓ |
| FuzzDecodeCatLogRequest | Catlog request (handle + clear flag) | ✓ |
| FuzzServiceNameSemantics | Wire-vs-loader consistency (NUL rejection, etc.) | — (semantic) |

### Autofs (autofs_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzParseV5Packet | Autofs v5 kernel notification (binary) |
| FuzzParseMountUnit | .mount config file parser |

### Fstab (fstab_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzFstabParse | pkg/fstab.Parse + Options() + FindByFile — util-linux octal escapes, freq/passno defaults, multi-comma option strings |

### Journal binary format (journalbin_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzJournalBinaryDecodeHeader | pkg/journalbin.DecodeHeader — 240-byte SLJRNL01 header (magic, incompat_flags, offset/size fields) |
| FuzzJournalBinaryOpenReader | Full OpenReader → EntryOffsets → SeekRealtime → Iter pipeline on a staged temp file. Seed corpus includes real Writer-produced journals so the mutator starts on ENTRY_ARRAY chain code paths |

### Journal binary format robustness (journalbin_robust_fuzz_test.go)
Each input runs under a 10 s deadline and an allocation budget (256 bytes per input byte plus 64 MiB), so a hang or an unbounded allocation fails the input like a panic does.

| Target | What it fuzzes |
|--------|----------------|
| FuzzJournalBinaryReadAll | Every read path journalctl takes on an untrusted file: Iter, EntryOffsets + ReadEntryAt forwards and backwards, SeekRealtime, the sd API (Next/Previous, cursors, matches), OpenDir + MultiReader.Iter, and Verify with an FSS key |
| FuzzJournalBinaryWriterReopen | NewWriterWithOptions (plain and sealed) on arbitrary bytes as an existing file, three appends, Close, then a read back: the path slinit-journald takes reopening the day file |

### Process Attributes (process_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzParseCapabilities | Linux capability names → numbers |
| FuzzParseSecurebits | Securebits flag names → bitmask |
| FuzzParseDuration | Decimal seconds → time.Duration |
| FuzzParseSignal | Signal name/number → syscall.Signal |
| FuzzReadEnvFile | KEY=VALUE env-file + !clear/!unset/!import meta |
| FuzzReadEnvDir | runit-style env-dir (one file per var) |

### State machine (co-located in pkg/service)

| Package | Target | What it fuzzes |
|---------|--------|----------------|
| pkg/service | FuzzStateMachine | Drives a 3-service dep chain through pseudo-random sequences of {Start, Stop, Restart, ForceStop, PinStart, Unpin, ProcessQueues} operations. Asserts state stays in the legal enum range and TargetState remains a steady value at every step. Uses InternalService for zero fork/exec overhead — ~17k iterations / 30s let the mutator explore deep operation sequences a unit test wouldn't reach. Already surfaced one real semantic gap on the first burst (stop-through-pinned-chain wedge); see the header docstring for the "known finding" note. |

## In-package fuzz targets

Fuzz targets that need access to unexported internals live next
to their code as `_fuzz_test.go` files. Run per-package rather than
via `./tests/fuzz/`.

| Package | Target | What it fuzzes |
|---------|--------|----------------|
| pkg/hostname | FuzzDecodeValue | machine-info/os-release value decoder + round-trip through the encoder |
| pkg/hostname | FuzzLoadMachineInfo | Full load → save → reload round-trip on /etc/machine-info |
| pkg/hostname | FuzzParseOSRelease | /etc/os-release parser |
| pkg/timedate | FuzzReadZoneTab | /usr/share/zoneinfo/zone.tab enumeration; asserts no NUL / no path-escape zones surface to caller |
| pkg/timedate | FuzzValidateZone | Zone-name validator (last line of defense before filepath.Join with the zoneinfo dir) |
| pkg/journal | FuzzParseSyslog | syslog(3) datagrams PID 1 reads from /dev/log: priority, timestamp, `IDENT[PID]:` |
| pkg/process | FuzzMatchFDName | fd-store name glob, linear-time `*` matching |
| cmd/slinit-immortal-convert | FuzzParseImmortalYAML | immortal run.yml reader; must return, with an error if need be, on any input |
| cmd/slinit-tmpfiles | FuzzTmpfilesParseLine | systemd-tmpfiles.d(5) directive lines |
| cmd/slinit-sysusers | FuzzSysusersParseLine | systemd-sysusers.d(5) directive lines |
| pkg/config | FuzzParseSystemdUnit | the `.service` unit reader: [Unit], [Service] and [Install], 96 keys, with their value parsers (durations, sizes, capability sets, exit-status lists, ...) |
| cmd/slinit-runit-convert | FuzzAnalyzeRunScript | /etc/sv/`<svc>`/run shell script analyzer |
| cmd/slinit-runit-convert | FuzzParseChpst | chpst argument parser (~15 short flags with optional args) |
| cmd/slinit-openrc-convert | FuzzParseOpenrcScript | /etc/init.d/`<svc>` openrc-run script parser |
| cmd/slinit-openrc-convert | FuzzParseDepend | `depend()` body mini-DSL parser |
| cmd/slinit-sysvinit-convert | FuzzParseInittab | `/etc/inittab` reader (truncated lines, DOS line endings, stray colons) |
| pkg/control | FuzzParseShutdownTime | shutdown's time operand: refused, or a delay in 0..MaxShutdownDelay, and zero (an immediate shutdown) only from a spelling of "now" or zero minutes |
| cmd/slinit | FuzzScanSysArgs | the PID-1 kernel command-line scan: no stray word becomes a boot service unless an `-m`/`-o` is on the line (a `-s` that did so caused a reboot loop), no returned word is an option or an invalid name |
| cmd/slinit-journalctl | FuzzRenderSenderFields | every sender-chosen field (message, identifier, `_COMM`, hostname, unit) in each terminal format: no control character, DEL, C1 or invalid UTF-8 reaches the terminal; a newline cannot start an unindented line (short) or forge a field line (verbose) |
| cmd/slinit-killall5 | FuzzParseStat | `/proc/<pid>/stat` reader; a crafted process name must not shift the field indexing |

Run per-package:
```bash
go test -fuzz=FuzzParseSystemdUnit -fuzztime=30s ./pkg/config/
go test -fuzz=FuzzDecodeValue -fuzztime=30s ./pkg/hostname/
```

## Crash corpus

Found crashes are stored in `testdata/fuzz/<FuzzName>/` by `go test -fuzz`.
These are automatically replayed on `go test` (no `-fuzz` flag) to prevent
regressions.
