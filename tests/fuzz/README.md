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

### Process Attributes (process_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzParseCapabilities | Linux capability names → numbers |
| FuzzParseSecurebits | Securebits flag names → bitmask |
| FuzzParseDuration | Decimal seconds → time.Duration |
| FuzzParseSignal | Signal name/number → syscall.Signal |
| FuzzReadEnvFile | KEY=VALUE env-file + !clear/!unset/!import meta |
| FuzzReadEnvDir | runit-style env-dir (one file per var) |

## Crash corpus

Found crashes are stored in `testdata/fuzz/<FuzzName>/` by `go test -fuzz`.
These are automatically replayed on `go test` (no `-fuzz` flag) to prevent
regressions.
