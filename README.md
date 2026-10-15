# SpaceHound

A safe, friendly macOS terminal UI for finding disk space you can get back.
It's a single native binary with no dependencies, and it supports Apple
Silicon and Intel Macs.

SpaceHound looks for:

- **Large files** above a threshold (default 1 GiB).
- **Duplicates** above a threshold (default 50 MiB). Hard-link aliases are
  rejected, a sampled hash runs before full SHA-256, and recoverable bytes
  are counted once.
- **Likely caches** under known user cache roots (`~/Library/Caches`,
  `~/.cache`, ...), with age and size evidence.
- **Stale logs** that are old (default 90 days) and big enough to matter
  (default 1 MiB).

Every finding explains *why* it was flagged and gets a risk level:

| Risk        | Meaning                                                                 |
|-------------|-------------------------------------------------------------------------|
| `low`       | Strong evidence it is regenerable or disposable (for example an old app cache). |
| `review`    | Probably your data, or ambiguous. Look before acting.                   |
| `dangerous` | App data, databases, VM images, blockchain data or developer runtimes. Review it by hand. |

Databases, VM images, blockchain data, `~/Library/Application Support` and
developer toolchains are **never** classified as disposable cache. A file
with a `.log` extension isn't treated as safe just because of its name.

For large files you have two remedies:

- **Move to Trash**: Finder-style. It is never a permanent delete.
- **Offload to an external drive**: copy, verify, and only then remove the
  original (details below).

See [SAFETY.md](SAFETY.md) for the full safety model.

## Install

### Quick install

```sh
curl -fsSL https://github.com/myneid/spacehound/releases/latest/download/install.sh | sh -s -- --version latest
```

This installs to `~/.local/bin`. If you prefer to inspect the installer first:

```sh
curl -fsSLO https://github.com/myneid/spacehound/releases/latest/download/install.sh
less install.sh                      # read it first
sh install.sh --version v0.1.0       # installs to ~/.local/bin by default
```

The `install.sh` attached to each release and the copy in this source tree
already know the repository. `--version latest` installs the newest published
release.

`install.sh` downloads the archive for your CPU and its `SHA256SUMS` file. It
refuses to install unless the checksum matches. To install to
`/usr/local/bin` instead, pass `--prefix /usr/local/bin`. It only uses `sudo`
when you choose that directory and it isn't writable, and even then it uses
`sudo` only for the final `install` step.

### From source

Requires Go 1.22 or newer. No third-party modules are needed.

```sh
make build          # produces ./spacehound
./spacehound --demo # try it on a generated fixture first
```

## Usage

```sh
spacehound                       # scan your home directory, open the UI
spacehound --root ~/Downloads    # scan one directory
spacehound --large 500MB --quick # lower threshold, skip duplicate hashing
spacehound --exclude node_modules --exclude ~/VMs
spacehound --json > report.json  # noninteractive, never changes anything
spacehound --demo                # safe demo on a temporary fixture
spacehound --show-defaults       # list the built-in skip rules
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--root PATH` | home | Directory to scan. Symlinks are never followed. |
| `--large SIZE` | `1GiB` | Large-file threshold. Accepts `500MB`, `2G`, `1.5GiB` (`K/M/G/T` and `KiB`... are binary, `KB/MB/GB/TB` are decimal). |
| `--dup-threshold SIZE` | `50MiB` | Minimum size for duplicate detection. |
| `--stale-days N` | `90` | Age before a log counts as stale. |
| `--no-duplicates`, `--quick` | off | Skip duplicate detection. |
| `--exclude PATTERN` | none | Repeatable. A path (contains `/` or starts with `~`) or a base-name glob. |
| `--include PATH` | none | Repeatable. Scan a path that an *overridable* default rule skips. |
| `--cross-devices` | off | Descend into other mounted filesystems. |
| `--json` | off | Print a JSON report to stdout and exit. |
| `--demo` | off | Scan a generated temporary fixture instead of real data. |
| `--no-color` | off | Disable colour (`NO_COLOR` also works). |
| `--version`, `--help` | | |

While a scan runs, progress shows on stderr. Press Ctrl-C to stop cleanly;
you get the partial results, clearly marked as partial.

### Keys

| Key | Action |
|-----|--------|
| `↑/↓`, `j/k`, `PgUp/PgDn`, `g/G` | Move |
| `Enter` / `→` | Details for the current item |
| `space` | Select or deselect |
| `a` / `n` | Select all visible (except dangerous) / clear the selection |
| `c`, `Tab`, `0`–`4` | Category filter |
| `r` / `s` / `/` | Risk filter / sort / path text filter |
| `t` (or `d`) | Move to Trash after you type `trash` |
| `o` | Offload to an external drive after you type `offload` |
| `f` | Refresh internal free space |
| `?` / `q` | Help / quit |

### Offload to an external drive

1. SpaceHound lists `/Volumes` with each volume's free space and whether
   it's writable. It rejects drives that are read-only, internal, network,
   hidden, Time Machine/backup, or otherwise unsuitable, and shows the reason.
2. The destination is
   `<volume>/SpaceHound Archive/<hostname>/<YYYY-MM-DD>/<path relative to home>`.
   SpaceHound checks free space (with headroom) before it copies. It never
   overwrites an existing file.
3. It copies with `/usr/bin/ditto`, which preserves macOS metadata. Then it
   checks that the destination is a regular file and that its byte count
   and SHA-256 match the source. It also re-checks that the source hasn't
   changed since the scan.
4. Only after verification does it ask, as a separate step, whether to
   `remove` the originals, move them to the `trash`, or keep both.

If anything fails, the original stays where it is and SpaceHound shows you
any partial copy so you can inspect it.

### Operation log

Every mutating operation is appended as JSON lines to
`~/Library/Logs/SpaceHound/operations.log` (mode 0600). Each entry records
paths, sizes, hashes, outcomes and free space. File contents are never
logged.

## Default skip rules

SpaceHound never scans the Trash, Time Machine and backup structures,
Spotlight and other filesystem metadata, or OS areas such as `/System`,
`/usr` and `/private/var/db`. By default it also skips the following; you
can scan them with `--include`:

- iCloud Drive and File Provider internals (`~/Library/Mobile Documents`,
  `~/Library/CloudStorage`, ...). Cloud placeholders ("dataless" files) are
  never read, because reading them would trigger a download.
- iPhone/iPad backups, Photos libraries and `.sparsebundle` internals.
- `/Volumes` when you scan from `/`.

Run `spacehound --show-defaults` for the exact list.

## Free space and APFS

The header shows internal free space before and after operations. If less
space appears than expected, SpaceHound explains why: the Trash isn't
emptied yet, local snapshots, files still held open by an app, or APFS
clones and hard links. It also prints read-only diagnostic commands you can
run (`tmutil listlocalsnapshots /`, `lsof +L1`, ...). It **never** deletes
snapshots.

"Likely savings" is conservative. It uses allocated bytes and counts zero
for hard-linked files. Sparse files and APFS clones aren't promised as
physical savings.

## Demo and verification

`--demo` builds a deterministic fixture tree in the OS temp directory: a fake
home, a fake `/Volumes` with one usable drive and two unsuitable ones, a
private Trash folder and a private log directory. All scanning, trashing and
offloading in demo mode stays inside that tree, and the tree is deleted on
exit.

```sh
make demo-json   # spacehound --demo --json
make demo        # interactive UI on the fixture
```

Automated tests cover parsing, classification, duplicates, identity
revalidation, Trash collisions, volume suitability, destination
construction, copy verification, key decoding and CLI flag wiring, all on
temporary fixtures. The interactive UI (`internal/tui`) has no automated
tests yet. Check it by hand with `make demo`: browse, filter, trash an item,
offload to "Demo Drive", and confirm that "Time Machine" and "Read Only" are
rejected.

## License

MIT. See [LICENSE](LICENSE).
