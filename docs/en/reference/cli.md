# CLI reference

```text
tidyshare <command> [flags]
```

| Command | Purpose |
| --- | --- |
| `scan` | read-only scan; writes reports to `-out` |
| `apply` | act on a reviewed `dups.tsv` (dry run unless `-yes`) |
| `version` | print version and commit |

## `scan`

| Flag | Default | Description |
| --- | --- | --- |
| `-root` | none, required | tree to scan |
| `-out` | `tidyshare-report` | report directory |
| `-min-size` | `1048576` | ignore smaller files for duplicate detection (bytes) |
| `-settle` | `1h` | skip files modified more recently for duplicate detection |
| `-stale-days` | `180` | mtime older than this marks a file cold |
| `-workers` | `8` | parallel hashers and MIME readers |
| `-depth` | `3` | directory summary depth under the root |
| `-skip` | empty | comma-separated absolute directories to skip |
| `-images` | `true` | list podman images and match image archives |
| `-engine` | `podman` | container engine binary; only podman JSON is parsed |
| `-image-users` | empty | rootless users whose storage is also listed |
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
