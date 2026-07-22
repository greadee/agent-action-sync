# Testing

## Required Toolchain

Local development requires Go 1.22 or newer.

Check the local toolchain:

```powershell
go version
gofmt -h
```

Run the suite:

```powershell
tools\test.ps1
```

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

CI installs Go from `go.mod`, checks `gofmt`, and runs:

```powershell
go test ./...
```

This keeps the test suite runnable even when a local workstation does not yet have Go installed.
