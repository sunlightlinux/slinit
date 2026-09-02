# Fuzz Tests

Go native fuzz tests (`testing.F`) for slinit's input parsing surfaces.

## Running

```bash
# Run a specific fuzz target for 30 seconds
go test -fuzz=FuzzConfigParse -fuzztime=30s ./tests/fuzz/

# Run all fuzz targets briefly (seed corpus only)
go test ./tests/fuzz/

# Run with extended time for deeper coverage
go test -fuzz=FuzzReadPacket -fuzztime=5m ./tests/fuzz/

# Run all fuzz targets sequentially
for f in $(go test -list 'Fuzz.*' ./tests/fuzz/ 2>/dev/null | grep ^Fuzz); do
    echo "=== $f ==="
    go test -fuzz=$f -fuzztime=30s ./tests/fuzz/
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
| FuzzConfigParse | Main service config file parser (text grammar). Seed corpus includes every real config from `demo/services/` (52 files as of v2.2.3) so the mutator starts from production-shaped inputs. Post-parse the desc's slice/map fields are traversed to catch partial-initialisation nil-deref shapes. |
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

### Process Attributes (process_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzParseCapabilities | Linux capability names → numbers |
| FuzzParseSecurebits | Securebits flag names → bitmask |
| FuzzParseDuration | Decimal seconds → time.Duration |
| FuzzParseSignal | Signal name/number → syscall.Signal |
| FuzzReadEnvFile | KEY=VALUE env-file + !clear/!unset/!import meta |
| FuzzReadEnvDir | runit-style env-dir (one file per var) |

## In-package fuzz targets

Fuzz targets that need access to `main`-package internals live next
to their code as `_fuzz_test.go` files. Run per-package rather than
via `./tests/fuzz/`.

| Package | Target | What it fuzzes |
|---------|--------|----------------|
| cmd/slinit-hostnamectl | FuzzDecodeValue | machine-info/os-release value decoder + round-trip through encodeValue |
| cmd/slinit-hostnamectl | FuzzLoadMachineInfo | Full load → save → reload round-trip on /etc/machine-info |
| cmd/slinit-hostnamectl | FuzzParseOSRelease | /etc/os-release parser |
| cmd/slinit-timedatectl | FuzzReadZoneTab | /usr/share/zoneinfo/zone.tab enumeration; asserts no NUL / no path-escape zones surface to caller |
| cmd/slinit-timedatectl | FuzzValidateZone | Zone-name validator (last line of defense before filepath.Join with zoneinfoDir) |
| cmd/slinit-tmpfiles | FuzzTmpfilesParseLine | systemd-tmpfiles.d(5) directive lines |
| cmd/slinit-sysusers | FuzzSysusersParseLine | systemd-sysusers.d(5) directive lines |
| cmd/slinit-systemd-convert | FuzzParseSystemdUnit | .service/.socket/.mount INI-shaped parser (~200 systemd directives, each with domain-specific value parsers) |
| cmd/slinit-runit-convert | FuzzAnalyzeRunScript | /etc/sv/`<svc>`/run shell script analyzer |
| cmd/slinit-runit-convert | FuzzParseChpst | chpst argument parser (~15 short flags with optional args) |
| cmd/slinit-openrc-convert | FuzzParseOpenrcScript | /etc/init.d/`<svc>` openrc-run script parser |
| cmd/slinit-openrc-convert | FuzzParseDepend | `depend()` body mini-DSL parser |

Run per-package:
```bash
go test -fuzz=FuzzParseSystemdUnit -fuzztime=30s ./cmd/slinit-systemd-convert/
go test -fuzz=FuzzDecodeValue -fuzztime=30s ./cmd/slinit-hostnamectl/
```

## Crash corpus

Found crashes are stored in `testdata/fuzz/<FuzzName>/` by `go test -fuzz`.
These are automatically replayed on `go test` (no `-fuzz` flag) to prevent
regressions.
