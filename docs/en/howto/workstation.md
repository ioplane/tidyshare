# Inspect a macOS workstation

The workstation changes require a source build from this revision. Published
`v0.1.0` assets support Linux only. Build native macOS binaries with CGO enabled
for the system `libproc` interfaces:

```sh
CGO_ENABLED=1 go build -tags remote,exclude_graphdriver_btrfs,containers_image_openpgp -o ./tidyshare ./cmd/tidyshare
./tidyshare doctor -root "$HOME/project" -engine docker -docker-context orbstack -json
```

`doctor` writes observations to stdout. It does not change data, start a runtime,
install tools, request privileges or change the Docker context. An unavailable
capability includes a reason. Available package-manager executables do not prove
that cache cleanup is safe.

## Inventory first

The `workstation` command inspects known cache locations inside a user data root.
It does not recursively inspect the home directory. Add each project directory explicitly:

```sh
./tidyshare workstation -root "$HOME" -project-root "$HOME/project/personal/tidyshare" -out /absolute/path/to/new-report
./tidyshare workstation -root "$HOME" -engine docker -docker-context orbstack -out /absolute/path/to/next-report -previous /absolute/path/to/new-report/workstation.json
```

The command writes private `workstation.json` and `workstation.md` files.
It includes Go, npm, uv, Homebrew, Chrome cache and Xcode paths where applicable.
Other recognized package caches remain visible. Missing optional paths are absent.
An incomplete observation retains its reason and returns a nonzero exit status.
Unknown sizes remain unknown. A partial scan does not establish a zero-sized cache.

Use `-min-free-bytes` to set a filesystem budget for the current workflow.
There is no default budget. The report compares available bytes with that budget.
It does not predict how much space a cleanup operation will recover.
Previous reports provide deltas only when the relevant identities and policies match.
Observed growth does not identify the process that caused it.

The MCP tool `inventory_workstation` uses the same collector.
Its `project_roots` and `previous` inputs cannot expand the startup scope.
Set `include_runtime=true` to inspect the runtime fixed at startup.
The tool does not use the strict cleanup guard to infer consumer inactivity.
This aggregate supports Docker runtime inventory. Podman returns an unsupported observation.
Use the existing `images` command for Podman inventory.

Browser profiles, Photos and application user data are protected.
Chrome disk cache uses a separate location. Temporary application bundles remain outside cleanup.
Inventory reports are observations and do not authorize deletion.

Use new report destinations for each run:

```sh
./tidyshare discover -root "$HOME/project" -profile workstation -out ./project-review
./tidyshare discover -root "$HOME/Library/Caches" -profile workstation -out ./cache-review
./tidyshare discover -root "$HOME/.cache" -profile workstation -out ./unix-cache-review
./tidyshare images -engine docker -docker-context orbstack -out ./image-review
```

The `workstation` profile protects known credentials, agent state, model stores,
the OrbStack backing store and the `~/OrbStack` data view. Use repeated
`-protect /absolute/path` flags for relocated storage. Protected paths take
precedence over custom cache roots and duplicate candidates. These defaults are
not an exhaustive inventory of application data.

`discover` reads Git metadata and measures recognized caches. It does not remove
worktrees or branches. Missing, locked, prunable and old trees need a separate
ownership and recovery check. They do not authorize deletion.

Homebrew caches and Xcode DerivedData are inventory records only. Maven local
packages and unsupported managers remain protected from cache cleanup.

## Inspect duplicates

```sh
./tidyshare scan -root /absolute/path/to/selected-data -profile workstation -images=false -out ./file-review
./tidyshare apply -plan file-review/dups.tsv -mode quarantine -quarantine /absolute/path/to/quarantine -profile workstation
```

Review every selected path. Stop consumers before execution. The default apply
operation is a dry run. `-yes` permits changes and `-limit 1` selects a canary.
Darwin duplicate deletion refuses nonempty resource forks. Scan hashes cover the
main data fork. Quarantine preserves resource forks and extended attributes.
Quarantine stays on the source filesystem. It preserves data but does not free
its storage. Explicit duplicate deletion is a separate `cleanup` command.

Use the same profile and additional protected roots when executing a duplicate
plan. Its TSV format does not contain policy settings. MCP preserves its startup
policy when it invokes child commands.

On macOS, extent deduplication is unsupported. The utility does not replace it
with APFS clones or hardlinks. Clone replacement can change file identity and
metadata semantics.

## Cache cleanup qualification

The separate [managed cleanup contract](managed-cleanup.md) provides scoped native operations and reviewed file manifests.
It does not change the strict process guard described below.
An inventory `proposed_operation` of `managed_plan` identifies a planning route. It does not authorize execution.

Darwin cache execution is not qualified. The process guard refuses incomplete
visibility, including unresolved memory submaps and inaccessible processes.
Successful FD, cwd, mapping and fileport refusal tests do not prove successful
idle-cache cleanup. Do not interpret skipped targets as cleaned targets.

The guard does not establish a global lock. A new consumer can start after a
check. Keep consumers stopped. uv uses its native locking on Darwin and does
not receive `--force`.

New cache plans use schema version 2. They bind the platform and protected roots
as well as the target identity. Darwin refuses legacy plans. Linux still accepts
version 1 under its existing validation rules.

## Docker and MCP

OrbStack uses the Docker provider. Inventory includes images, container references
and aggregate volume/build-cache statistics. Archive matching uses established
configuration identities. An unestablished multi-platform identity makes an
unmatched archive unknown. Runtime presence is not a backup.

Docker inventory accepts local Unix sockets. Context selection follows Docker
precedence: an explicit context, `DOCKER_CONTEXT`, `DOCKER_HOST`, then the current
CLI context. No operation changes the global context. MCP fixes the selected
socket and daemon identity at startup.

```sh
./tidyshare mcp -root "$HOME/project" -out "$HOME/tidyshare-reports" -profile workstation -engine docker -docker-context orbstack
```

MCP exposes `inventory_images`. Docker image cleanup and cleanup planning remain
unsupported. Podman retains its existing reviewed-plan cleanup. This stage does
not prune volumes, containers, networks or build cache.

## Read storage figures correctly

Logical bytes measure file lengths. Allocated bytes measure filesystem blocks,
with hardlinks counted once in the scan summary. Shared APFS extents can appear
in multiple files. Neither value predicts recovered space.

Do not add Docker layer totals, build-cache totals and backing-image sizes.
These measurements can overlap. Compare filesystem free-space observations
before and after an approved operation, while accounting for concurrent writes.
`coverage.json` reports incomplete file scans. Review its warnings before using
a plan. User paths with symlink components remain refused. Internal temporary
directories resolve the operating system's temporary-path alias first.
