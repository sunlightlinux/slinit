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

## Targets

### Config Parsing (config_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzConfigParse | Main service config file parser (text grammar) |
| FuzzParseIDMapping | Namespace UID/GID mapping "container:host:size" |
| FuzzParseCPUAffinity | CPU affinity spec "0-3 8-11" |
| FuzzParseLSBHeaders | /etc/init.d LSB header block parser |

### Control Protocol (protocol_fuzz_test.go)
| Target | What it fuzzes |
|--------|----------------|
| FuzzReadPacket | Binary packet reader [type(1)+len(2)+payload(N)] |
| FuzzDecodeServiceName | Service name [len(2)+name(N)] |
| FuzzDecodeHandle | uint32 handle |
| FuzzDecodeServiceStatus | 12-byte service status |
| FuzzDecodeServiceStatus5 | 14-byte v5 service status |
| FuzzDecodeSetEnv | Set-env request (handle + KEY=VALUE) |
| FuzzDecodeEnvList | Env list reply (KEY=VALUE\0 pairs) |
| FuzzDecodeDepRequest | Add/remove dependency request |
| FuzzDecodeBootTime | Boot timing info |

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
