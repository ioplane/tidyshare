# Review managed cleanup

Managed cleanup uses a separate contract. It does not change the strict Darwin guard or its `UNKNOWN/BLOCKED` results.
Its observations do not establish global consumer inactivity. A new reader or writer can appear after an observation.
Keep required consumers stopped yourself. The utility does not close applications or end sessions.

## Supported scopes

| Kind | Operation | Limits |
|---|---|---|
| `go_build` | Native `go clean -cache` | Build cache only, no module cache |
| `npm` | Native cleanup of the selected `_cacache` | Isolated configuration and logs, scripts disabled |
| `homebrew` | Reviewed regular download files older than 24 hours | No generic `brew cleanup` |
| `chrome` | `Cache` and `Code Cache` under recognized cache profiles | Profiles and temporary application bundles excluded |
| `xcode` | DerivedData `Build/Intermediates.noindex` and `Logs` | Products, installed applications and source excluded |
| `docker_build` | Plan for old daemon build cache | Execution blocked until disposable daemon and builder qualification |

Application file adapters use macOS directory layouts. Other layouts need separate qualification.
uv remains outside managed execution because external symlink consumers can depend on its cache.

Native qualification checks the installed command on private fixtures.
It verifies removal inside the selected fixture and preservation of an outside marker.
The plan records manager identity, digest and version. npm also records its Node interpreter identity and digest.
Execution repeats qualification and rejects a changed executable or interpreter.
This qualification does not prove that all external consumers can survive cleanup.

## Create and inspect a plan

```sh
./tidyshare managed-plan -root "$HOME" -kind homebrew -out /absolute/path/to/new-plan
./tidyshare managed-clean -plan /absolute/path/to/new-plan/managed-plan.json -out /absolute/path/to/new-dry-run
```

Repeat `-kind` to select categories. Omitting it includes all managed categories.
Use `-engine docker -docker-context orbstack` to record the selected daemon identity.
This option reads runtime inventory and does not enable prune.
Inspect blocked and absent targets. They do not represent completed cleanup.

Execution requires the exact reviewed plan digest and explicit target paths:

```sh
./tidyshare managed-clean -plan /absolute/path/to/new-plan/managed-plan.json -plan-sha256 "$REVIEWED_PLAN_SHA256" -target /absolute/target/from/the/plan -limit 1 -yes -out /absolute/path/to/new-canary-results
```

The default operation is a dry run. `-limit 1` limits manifest entries or native operations.
A skipped entry can consume that limit. Native cleanup applies its manager scope, not a file whitelist.
The JSON plan uses schema version 1 and contract `managed`. Legacy cache plans are rejected.

## Read results and limits

File manifests exclude fresh files, symlinks, hardlinks and known lock names.
Git project markers and bare repositories block a cache category, including projects added after planning.
Execution checks inode, device, owner, mode, size and file times.
It claims each file in a private directory before unlink. New or changed entries are preserved.
If restoration fails, the result gives the retained file's recovery path.
Directory movement and concurrent writes still prevent a universal race-free guarantee.

Visible application processes and open-file observations can block an operation.
An unavailable required observer also blocks it. These checks do not cover all memory mappings or future opens.

Reports use private permissions. They include the exact reviewed plan and a synchronized intent/result journal.
The executor synchronizes directory entries before cleanup. Each file intent records its generated recovery path before rename.
These barriers acknowledge operating-system writes. They do not guarantee hardware recovery after power loss.
If journal writes fail, the executor stops further actions. Review partial results before any retry.
Native results retain command status and before/after file counts, without a per-file native deletion journal.
Filesystem observations remain separate. A free-space change is not attributed reclaim and can include concurrent writes.

MCP exposes `plan_managed_cleanup` and `cleanup_managed` through the same implementation.
Execution also requires startup `-allow-cleanup` and the exact `plan_sha256` and `targets` inputs.
The tools cannot expand the startup root or remove protected data.

Qualification uses only disposable fixture data. It does not authorize cleanup of a workstation's active caches.
