# Reclaim space from duplicates

Apply a reviewed `dups.tsv`. Dry run is the default.

## 1. Review the plan

Open `dups.tsv`. Each group has one `keep` row and one or more `dup` rows.
Swap the actions where the kept copy should live somewhere else, and delete
duplicate rows you want to leave alone. Remove the whole group if no duplicate
remains. Preserve the header, five tab-separated columns, quoted absolute
paths and final newline. Each path appears only once, and all rows in a group
have the same positive size.

## 2. Dry run

```bash
tidyshare apply -plan dups.tsv -mode dedupe -quarantine /srv/share/.tidyshare
```

The command prints one line per proposed change and a summary with the free
space before and after. It creates no directory or manifest and changes no file.
When the quarantine does not exist, the existing parent supplies the filesystem
and free-space checks. Dry run does not run the dedupe ioctl or prove that a
later write will succeed.

## 3. Canary

```bash
tidyshare apply -plan dups.tsv -mode dedupe -quarantine /srv/share/.tidyshare -limit 1 -yes
```

Check the result:

```bash
cmp "$KEEP" "$DUP" && echo identical
stat -c '%y %U %a' "$DUP"     # mtime, owner and mode are unchanged
xfs_io -c fiemap "$KEEP" "$DUP"   # the same physical extents
```

## 4. Apply

```bash
tidyshare apply -plan dups.tsv -mode dedupe -quarantine /srv/share/.tidyshare -yes
```

`/srv/share/.tidyshare/MANIFEST.tsv` records completed changes. A manifest open,
write, sync or close failure returns a nonzero exit code and stops the run.
A write or sync failure can follow a completed change: inspect the named paths
before retrying, since the operation and manifest are not one transaction.

## Choose a mode

| Mode | Effect | Paths | Space freed |
| --- | --- | --- | --- |
| `dedupe` | the duplicate shares extents with the kept copy | all stay | duplicate private extents can be released immediately |
| `quarantine` | the duplicate is moved under `-quarantine` | the duplicate path disappears | after you remove the quarantine |

Use `dedupe` when other software may refer to the duplicate path. Use
`quarantine` when a copy is genuinely unwanted; remove the quarantine yourself
after a soak period.

Logical duplicate bytes do not measure physical recovery. Repeating dedupe is
safe, but already shared extents do not release more space. Compare filesystem
free space and FIEMAP before and after on a quiet filesystem. Shared extents
remain allocated until their last reference is removed.

## Refusals

The entire plan is validated before directories or files are created. A malformed
or oversized row, missing final newline, invalid action or number, repeated path,
relative or unclean path, missing keeper, extra keeper or inconsistent group size
rejects the plan. Do not put source paths inside the quarantine.

A duplicate is skipped, and counted as `skipped`, when its size or mtime differs
from the plan, when the keeper disappears or changes, when a source is not a
regular file, when a source path contains a symlink, or when either source is on a
different filesystem than the quarantine. Mtime comparisons have one-second
precision. The kernel compares bytes for dedupe and refuses different content;
quarantine relies on the reviewed plan and the size/mtime checks.

Quarantine and manifest paths must not contain symlinks. The manifest must be a
regular file with one link. Quarantine refuses an existing destination and uses
a no-replace rename, so retrying cannot overwrite a quarantined file. Refused
files remain in place; inspect `skipped` and warnings even when the exit code is
zero. Dedupe can share earlier chunks before a later chunk is refused, while
preserving file contents.

Stop writers and directory renames during review and apply. Path checks do not
provide a transaction against concurrent replacement of parent directories;
this command is for reviewed, quiet paths. A file rewritten with the same size
and restored mtime can pass the metadata check. Dedupe still compares current
bytes, but quarantine does not independently re-hash them.
