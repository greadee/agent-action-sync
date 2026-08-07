# Testing

## Required Toolchain

Local development requires Go 1.25 or newer.

Check the local toolchain:

```powershell
go version
gofmt -h
```

Run the suite:

```powershell
tools\test.ps1
```

The script also supports a local portable Go layout:

```text
.tools/go1.25.12/go/bin/go.exe
```

When that exists, the script uses it automatically and stores build caches under `.cache/`.

If formatting needs to be skipped temporarily while diagnosing a toolchain issue:

```powershell
tools\test.ps1 -SkipFormat
```

## CI Guarantee

GitHub Actions runs the same core suite on every push to `main` and `codex/**` branches, and on pull requests into `main`.

The workflow is defined in:

```text
.github/workflows/go-test.yml
```

CI installs Go from `go.mod`, checks `gofmt` for `cmd` and `internal`, and runs:

```powershell
go test ./...
```

This keeps the test suite runnable even when a local workstation does not yet have Go installed.

## Reliability and security checks

Before merging synchronization changes, also run the focused race detector:

```powershell
go test -race ./internal/sync ./internal/transfer ./internal/filesystem
```

The repository includes fuzz targets for relative-path normalization and ignore matching. A short smoke run is useful before a merge:

```powershell
go test -run '^$' -fuzz=FuzzNormalizeRelativePath -fuzztime=5s ./internal/filesystem
go test -run '^$' -fuzz=FuzzMatchIgnorePattern -fuzztime=5s ./internal/sync
```

Fault-injection tests cover write and flush failures that represent disk-full behavior. Locked-file behavior remains filesystem- and sharing-mode-specific, so it is exercised manually on supported platforms rather than asserted in portable CI.
