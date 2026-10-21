# CLI reference

```text
tidyshare <command> [flags]
```

| Command | Purpose |
| --- | --- |
| `scan` | read-only scan; writes reports to `-out` |
| `apply` | act on a reviewed `dups.tsv` (dry run unless `-yes`) |
| `images-plan` | prepare an exact owner-bound orphan image plan |
| `cleanup` | delete reviewed copies or orphan images (dry run unless `-yes`) |
| `mcp` | expose local operations over stdio within startup boundaries |
| `version` | print version and commit |

## `scan`

| Flag | Default | Description |
| --- | --- | --- |
| `-root` | none, required | tree to scan |
| `-out` | `tidyshare-report` | report directory |
| `-formats` | `json,jsonl,md` | comma-separated `json,jsonl,md`; legacy TSV plans remain available |
| `-min-size` | `1048576` | ignore smaller files for duplicate detection (bytes) |
| `-settle` | `1h` | skip files modified more recently for duplicate detection |
| `-stale-days` | `180` | mtime older than this marks a file cold |
| `-workers` | `8` | parallel hashers and MIME readers |
| `-depth` | `3` | directory summary depth under the root |
| `-skip` | empty | comma-separated absolute directories to skip |
| `-images` | `true` | list podman images and match image archives |
| `-engine` | `podman` | deprecated compatibility flag; only `podman` is accepted and no binary is executed |
| `-image-users` | empty | rootless users whose storage is also listed |
| `-image-sockets` | empty | comma-separated requested `owner=unix:///path` API endpoint overrides |
| `-yara-rules` | empty | rules file or directory; empty disables YARA |
| `-yara-bin` | `yara` | classic yara CLI with `--scan-list` |
| `-yara-cats` | `binary,installer,package,firmware,dump` | categories to scan |
| `-yara-max-size` | `536870912` | skip larger files in the YARA scan (bytes) |

## `apply`

| Flag | Default | Description |
| --- | --- | --- |
| `-plan` | none, required | `dups.tsv` from `scan` |
| `-quarantine` | none, required | directory on the same filesystem as the files; holds `MANIFEST.tsv` |
| `-mode` | `quarantine` | `quarantine` moves duplicates; `dedupe` shares extents |
| `-limit` | `0` | stop after N files (canary run); 0 means all |
| `-yes` | `false` | perform the changes; without it nothing is modified |

## `cleanup`

`-kind duplicates|images`, `-plan` and `-out` are required. `-formats` selects
one or more of `json,jsonl,md` (default `md`). `-yes` performs deletion;
without it the command only reviews the targets and writes reports. `-limit`
limits processed targets (default `0`, all). Duplicate plans use `dups.tsv`
(up to 64 MiB); image plans use strict `image-plan.json` (up to 8 MiB).
Skips are recorded even when the exit status is zero. A failed operation stops
and writes the accumulated result where possible. Existing report files are
never replaced; use a separate directory for each run.

## `images-plan`

`-out` is required. `-owner` defaults to `root`; `-socket` optionally selects
that owner's local Unix endpoint. `-formats` has the same choices as cleanup.
The command writes `image-plan.json` plus the selected review reports.
The plan binds exact IDs to the owner's actual graphroot and runroot.
No image is deleted by this command.

## `mcp`

`-root` and `-out` are required and must be distinct, non-overlapping directories.
`-allow-cleanup` permits explicit execution requests; the default permits dry
runs only. `-image-owner` defaults to `root`; `-image-socket` fixes the local
API endpoint. Tool requests cannot select a different owner or endpoint.
See [MCP](../howto/use-mcp.md) for tool inputs and client configuration.

## Exit status

| Code | Meaning |
| --- | --- |
| 0 | completed |
| 1 | error (bad flags, unreadable plan, report write failure) |
| 2 | unknown command |

`scan` accepts a real directory as `-root` and normalizes it to an absolute
path so duplicate plans can be applied from another directory. A missing
root, a regular file or a root symlink is rejected before reports are
created. `-workers` and `-min-size` must be positive; `-settle`, `-stale-days`
and `-depth` must be non-negative.
