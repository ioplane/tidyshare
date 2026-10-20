# Reclaim space from duplicates

Apply a reviewed `dups.tsv`. Dry run is the default.

## 1. Review the plan

Open `dups.tsv`. Each group has one `keep` row and one or more `dup` rows.
Swap the actions where the kept copy should live somewhere else, and delete
rows you want to leave alone.

## 2. Dry run

```bash
tidyshare apply -plan dups.tsv -mode dedupe -quarantine /srv/share/.tidyshare
```

The command prints one line per change and a summary with the free space before
and after.

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

`/srv/share/.tidyshare/MANIFEST.tsv` records every change.

## Choose a mode

| Mode | Effect | Paths | Space freed |
| --- | --- | --- | --- |
| `dedupe` | the duplicate shares extents with the kept copy | all stay | when the last sharer is removed |
| `quarantine` | the duplicate is moved under `-quarantine` | the duplicate path disappears | after you remove the quarantine |

Use `dedupe` when other software may refer to the duplicate path. Use
`quarantine` when a copy is genuinely unwanted; remove the quarantine yourself
after a soak period.

## Refusals

A row is skipped, and counted as `skipped`, when the file's size or mtime
differs from the plan, when it is not a regular file, or when it is on a
different filesystem than the `-quarantine` directory. The kernel refuses a
dedupe when the bytes differ.
