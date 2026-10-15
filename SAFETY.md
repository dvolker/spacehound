# SpaceHound safety model

SpaceHound is built so that a mistake in the UI or the classifier costs you
time, not data. These guarantees are enforced in code and covered by tests.

## Scanning is read-only

- The scanner never writes, renames or removes anything. Selecting items
  never changes anything either.
- It never follows symlinks, and by default it doesn't cross into other
  filesystems.
- It skips the Trash, Time Machine and backup structures, cloud-storage
  internals, Photos libraries and OS areas (`spacehound --show-defaults`).
- It never reads cloud placeholder ("dataless") files, because reading them
  would trigger a download.
- `--json` mode has no mutating code path.

## Classification is cautious

- Risk levels are `low`, `review` and `dangerous`. When more than one
  detector matches a file, it gets the most cautious level among them.
- Databases, VM and disk images, blockchain data, application data
  (`~/Library/Application Support`, Containers, Mail, ...) and developer
  runtimes are never called disposable cache. Large files in those places
  are marked `dangerous`.
- A log is only `low` risk when it is old, large and inside `~/Library/Logs`.
  Matching the extension alone is never enough.
- Cache-like files that macOS manages, and downloaded ML models, are
  marked `review`.
- Selecting "all visible" skips `dangerous` items.

## Every action is confirmed and revalidated

- Each mutating action first shows a summary: the items, total size,
  likely savings and risk counts. Then you have to type a word: `trash`,
  `offload` or `remove`. Anything else cancels.
- Right before acting on each file, SpaceHound re-checks its device, inode,
  size and modification time against the scan. If the file changed, it
  refuses to touch it.

## Deletion means the Trash

- "Delete" always moves the file to the Trash on the same volume. It uses
  the macOS `trash` tool when present, or a native rename into `~/.Trash` or
  `<volume>/.Trashes/<uid>` with a collision-safe name. Nothing is ever
  permanently unlinked as a "delete".
- If no usable Trash exists, for example on a read-only volume or a volume
  without `.Trashes`, SpaceHound tells you why and does nothing.
- Space isn't reclaimed until you empty the Trash. The UI shows how much is
  waiting there.

## Offload is copy, verify, then remove

1. Only regular files can be offloaded; directories and live app databases
   cannot. A file with several hard links is refused, because offloading one
   name would free nothing.
2. Suitable destinations are writable, locally attached volumes under
   `/Volumes`. Read-only, internal, network, hidden and Time Machine/backup
   volumes are rejected, and the UI shows the reason. FAT volumes are
   limited to files under 4 GiB.
3. SpaceHound checks free space with headroom before copying.
4. The destination is
   `<volume>/SpaceHound Archive/<hostname>/<YYYY-MM-DD>/<relative path>`.
   Path traversal is rejected. Directories are created without following
   symlinks. An existing destination file is never overwritten.
5. It copies to a temporary name with `/usr/bin/ditto`, which preserves
   metadata and resource forks, with a pure-Go fallback. Then it checks
   that the destination is a regular file and that its byte count and
   SHA-256 match the source. It re-checks the source identity, and only
   then publishes the file under its final name.
6. Only after verification succeeds does SpaceHound ask a **separate**
   question: type `remove` to delete the originals (the verified copies stay
   on the drive), `trash` to move them to the Trash, or press Esc to keep
   both. Immediately before removing anything, it re-confirms both the
   source identity and the destination copy.
7. If anything goes wrong, the original stays where it is. Any partial
   copy is reported so you can inspect it by hand.

The `remove` choice in step 6 is the only place SpaceHound unlinks a file
directly. It only happens after a verified, revalidated copy exists on
another disk and you've typed the confirmation.

## Snapshots and free space

SpaceHound never deletes APFS or Time Machine snapshots and never runs
privileged commands. When freed space doesn't appear, it explains the
likely causes and prints read-only diagnostic commands.

## Logging

All mutating operations are recorded as JSON lines in
`~/Library/Logs/SpaceHound/operations.log` (0600). The log holds paths,
sizes, hashes, outcomes and free space. File contents are never logged.

## Demo mode

`--demo` works only on a generated fixture in the OS temp directory. Its
Trash, volumes and log are all inside that fixture, and it never uses the
system `trash` tool. The fixture is removed on exit, and the removal code
refuses to delete anything that doesn't look like a demo directory.
