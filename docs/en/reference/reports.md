# Report files

`scan -formats json,jsonl,md` writes reports to `-out`. This is the default;
select any comma-separated combination to omit other renderings. TSV plans
and optional YARA inputs remain available with every selection. Use a fresh
output directory: publication refuses existing target files and symlinks in
every directory component. New directories use mode `0700`; reports use `0600`.

| File | Content |
| --- | --- |
| `report.md` | totals; tables by category, MIME type, top-level area, directory and cold directory |
| `catalog.jsonl` | one JSON object per file: `path`, `area`, `category`, `mime`, `size`, `mtime`, `atime`, `stale` |
| `report.json` | versioned scan object containing the root and semantic records from the catalog and TSV tables |
| `report.jsonl` | versioned summary followed by one semantic record per line |
| `dups.tsv` | duplicate plan, columns `group`, `action`, `size`, `mtime`, `path` |
| `stale.tsv` | stale files, columns `reason`, `size`, `mtime`, `atime`, `path` |
| `candidates.md` | deletion candidates by tier, directory and file |
| `candidates.tsv` | `tier`, `kind`, `bytes`, `files`, `path`, `note` |
| `images.md`, `images.tsv` | podman images and archive matching (with `-images`) |
| `yara.tsv`, `yara.list`, `yara.err`, `yara-index.yar` | YARA results and inputs (with `-yara-rules`) |

Paths in TSV files and the candidate `note` column are Go-quoted strings.

## Structured scan reports

`report.json` contains `schema_version: 1`, `operation: "scan"`, `root`, and
`records`. Each record contains `schema_version: 1`, `type`, and `data`.
Types are `catalog`, `dups`, `stale`, `candidates`, `images`, and `yara` when
their source table is present. Catalog fields retain JSON numbers; TSV fields
are strings, with Go-quoted values decoded. The machine formats include every
table row rather than the largest-row limits of Markdown summaries.
Publication counts and validates staged rows first, then streams records into
the selected machine reports without retaining the complete record set.

`report.jsonl` starts with a `summary` record whose `data` contains `operation`,
`root`, and the number of following records. Subsequent records have the same
shape and meaning as `report.json.records`. `catalog.jsonl` remains the legacy
catalog stream and is emitted only when `jsonl` is selected.

For example, `scan -root /srv/share -out ./report -formats json` writes
`report.json`, `dups.tsv`, `stale.tsv`, `candidates.tsv`, and enabled image or
YARA TSV/input files. It omits all Markdown and JSONL files.

## Cleanup operation reports

Cleanup commands use the same `-formats` selection and write
`cleanup.json`, `cleanup.jsonl`, and `cleanup.md` to their selected output
directory. All destinations are reserved before an operation changes data;
an existing regular file, hardlink, symlink, or directory at a target is a
refusal. A write or sync failure remains an error even after cleanup has run;
inspect the operation result before retrying a mutation. Reserved files can
remain empty after an interrupted or rejected operation.

Schema version 1 contains `operation`, `dry_run`, `results`, `changed`,
`skipped`, and `logical_bytes`. Results contain `target`, `status`, and
optional `keeper`, `owner`, `reason`, and `bytes`. JSONL starts with a
`type: "summary"` record containing the totals and operation metadata, then
one `type: "result"` record per result; every record has `schema_version: 1`.
Markdown contains the same metadata, totals, and results. Logical bytes do
not measure physical space reclaimed. Paths and identifiers are local to the
explicitly selected scan or cleanup scope; reports are private local files.

## Developer storage reports

`discover` writes selected `discovery.json`, `discovery.jsonl` and
`discovery.md`, plus the always-retained `cache-plan.json`. JSON schema version 1
contains `operation: "discover"`, `root`, `caches`, `git_trees` and `warnings`.
Cache records include `kind`, `path`, logical `bytes`, regular `files`,
`device`, `inode`, `uid`, `cleanup` and an optional `reason`. The boolean
describes an implemented cleanup policy; execution still checks current
identity, occupancy, mounts and source-data guards. Git records include `path`,
`kind`, available `git_dir`, `common_dir`, `head`, `branch`, and boolean
`locked`, `prunable`, `missing`, plus an optional `reason`.

JSONL starts with a `summary` record and follows with `cache`, `git_tree` or
`warning` records. Every record has `schema_version: 1`, `type` and `data`.
Markdown renders the same inventory and warnings with escaped table cells.

The cache plan contains `schema_version: 1`, the discovery `root`, and `targets`
selected from caches whose cleanup policy is implemented. Each target binds its
kind and path to device, inode and owner. Editing the target list narrows scope;
it does not bypass current cache qualification or permit arbitrary commands.

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

## Image inventory

`images.tsv` keeps the columns `kind`, `status`, `size`, `ids`, `tags`,
`stores`, `path`. Unnamed non-dangling images use `local-untagged`.
A `store` row with status `unavailable` identifies a failed requested store
in the Go-quoted `stores` column; its diagnostic is in `images.md`.
Archive status `unknown` means no ID was found while at least one requested
store was unavailable. `absent` requires all requested stores to be queried
successfully. Only a complete positive `in-store` match qualifies for tier 2
review; neither status authorizes deletion.

Duplicate totals are logical file lengths, not a measurement of unique
allocated extents. Already shared extents, hardlinks and sparse files can
reduce the actual recovery. Measure filesystem free space before and after
an approved operation and compare file content and metadata.
