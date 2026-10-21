# Find language caches and Git trees

Use `discover` to inventory developer storage without hashing source files or
starting package managers:

```bash
tidyshare discover -root /srv/developers -out ./developer-review -formats json,jsonl,md
```

`discovery.md` lists cache kinds, paths, logical bytes, file counts and cleanup
eligibility. It also lists Git trees with their branch, available HEAD, lock,
missing and prunable indicators. JSON and JSONL contain the same records.
`cache-plan.json` contains only cache kinds with an implemented cleanup policy.
Review it and remove unwanted `targets` entries before cleanup.
The output directory must remain outside these cache targets; discovery
rejects an overlapping destination before creating report files.

The directory walk does not follow symlinks. It skips its output directory,
recognized cache payload trees and Git administrative directories. Git clones
inside managed caches belong to the cache scope; they are not listed as
developer working trees. Cache sizes count regular-file lengths, not unique
allocated extents or predicted recovered space.

Git inventory reads bounded metadata in Go: `.git` directories and pointer
files, common directories, HEAD/ref files and registered linked worktrees.
Kinds are `main`, `linked`, `gitfile` and `bare`. A `gitfile` can identify a
submodule or a repository initialized with `--separate-git-dir`; discovery does
not infer a submodule relationship from this pointer alone.
Repository configuration, hooks, indexes and object payloads are not loaded.
Locked or missing trees remain inventory records; these indicators never
authorize deletion. This command does not remove Git trees or branches.

Metadata outside the selected root is opaque and gets a reason in the record.
Packed or unborn refs can leave HEAD empty while preserving the branch name.
Metadata files are limited to 64 KiB and worktree registrations to 65,536 per
repository. Keep source directories quiet during discovery. Errors are written
as warnings; incomplete discovery returns exit status 1, even when some
positive cache targets were found. Review warnings before using such a plan.

See [Clean language caches](clean-language-caches.md) for supported kinds,
configuration boundaries and the dry-run/canary workflow, and
[Use MCP](use-mcp.md) for scoped tool access.
