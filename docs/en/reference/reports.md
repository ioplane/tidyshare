# Report files

`scan` writes these files to `-out`.

| File | Content |
| --- | --- |
| `report.md` | totals; tables by category, MIME type, top-level area, directory and cold directory |
| `catalog.jsonl` | one JSON object per file: `path`, `area`, `category`, `mime`, `size`, `mtime`, `atime`, `stale` |
| `dups.tsv` | duplicate plan, columns `group`, `action`, `size`, `mtime`, `path` |
| `stale.tsv` | stale files, columns `reason`, `size`, `mtime`, `atime`, `path` |
| `candidates.md` | deletion candidates by tier, directory and file |
| `candidates.tsv` | `tier`, `kind`, `bytes`, `files`, `path`, `note` |
| `images.md`, `images.tsv` | podman images and archive matching (with `-images`) |
| `yara.tsv`, `yara.list`, `yara.err`, `yara-index.yar` | YARA results and inputs (with `-yara-rules`) |

Paths in TSV files are Go-quoted strings.

## `dups.tsv`

`action` is `keep` for the copy to retain and `dup` for each redundant copy.
`apply` re-checks `size` and `mtime` against the file system before acting.

## Candidate kinds

| Tier | Kind | Meaning |
| --- | --- | --- |
| 1 | `dup-file` | byte-identical copy of a kept file |
| 1 | `junk-file` | editor backup, temporary or core-dump name |
| 1 | `redundant-dir` | every file in the directory is a duplicate |
| 2 | `image-archive` | image archive whose images are in local storage |
| 2 | `regen-dir` | cache directory (`node_modules`, `__pycache__`, `.venv`, ...) |
| 3 | `regen-dir` | build output (`target`, `build`, `dist`, `extracted`, ...) |
| 3 | `cold-dir` | top-most tree, 100 MiB or more, untouched for `-stale-days` |

## Stale reasons

| Reason | Rule |
| --- | --- |
| `junk` | name ends with `~`, is `core.<pid>`, starts with `.~lock`, or has a `.tmp`, `.part`, `.crdownload`, `.swp`, `.bak`, `.old`, `.orig` extension |
| `cold` | mtime older than `-stale-days`; `atime` is recorded but not used, because a backup or hash pass resets it |
