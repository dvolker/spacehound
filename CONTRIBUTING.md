# Contributing to SpaceHound

Thanks for helping. SpaceHound deletes and moves people's files, so safety
comes before features.

## Ground rules

- **Standard library only.** Please don't add third-party modules.
- **Never touch real data in tests.** Tests must only use `t.TempDir()` or
  the `internal/demo` fixture. Don't scan `$HOME`, `/Volumes` or the real
  Trash in tests, and don't run the binary against real data while
  developing. Use `--demo`.
- **Keep the safety model intact.** Any change to Trash, offload, identity
  checks or classification needs tests, and needs [SAFETY.md](SAFETY.md)
  updated if behaviour changes. New mutating actions must use a summary
  screen, a typed confirmation and `fsid.Revalidate` right before acting.
- **Be conservative when classifying.** If a file might be app data,
  mark it `review` or `dangerous`, not `low`.

## Workflow

```sh
make fmt        # gofmt
make vet        # go vet ./...
make test       # go test ./...
make build      # ./spacehound
make demo-json  # executable check on a temporary fixture
make demo       # interactive check on a temporary fixture
```

`make check` runs the formatting check, vet, tests and build together. It
must pass before you open a pull request.

## Layout

| Path | Purpose |
|------|---------|
| `cmd/spacehound` | Flags and wiring |
| `internal/scan` | Read-only walker, classifier, duplicates, skip rules |
| `internal/model` | Report types shared by JSON and the UI |
| `internal/fsid` | File identity capture and revalidation |
| `internal/trash` | Move to Trash (never unlink) |
| `internal/volumes` | External volume discovery and suitability |
| `internal/offload` | Verified copy-before-remove offload |
| `internal/oplog` | Operation log |
| `internal/diskspace` | Free space and APFS shortfall explanations |
| `internal/term`, `internal/tui` | Raw terminal and UI state machine |
| `internal/demo` | Deterministic temporary fixture |

## Commits

Use short conventional-style subjects (`feat:`, `fix:`, `docs:`, `test:`).
Explain the safety impact in the commit body when there is one.
