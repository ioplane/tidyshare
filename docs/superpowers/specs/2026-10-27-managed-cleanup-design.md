# Managed cleanup design

## Goal

Implement the approved second stage with a separate managed contract. Preserve the strict Darwin UNKNOWN/BLOCKED gate.
The first stage is commit 23634b7. Later Darwin research remains separate.

## Interface and contract

Add managed-plan and managed-clean commands. Add plan_managed_cleanup and cleanup_managed MCP tools.
Plans use schema version 1 and contract managed. Old cache plans are not accepted.
Planning accepts -root, -out, optional repeated -kind, and optional Docker context. The default includes all six categories.
Execution defaults to dry run. Actual changes require -yes, the exact plan SHA-256 and explicit repeated -target selections. Support -limit 1.
Validate root identity, owner, device, policy, platform, target scopes and all manifest entries. Reject unknown kinds and overlapping targets.
Native operations pin executable identity and version. Npm also pins its Node interpreter. Qualify commands on private temporary fixtures before planning and recheck before execution.

## Categories

- Go build: native go clean -cache. Do not clear module cache.
- npm: native cache clean for the selected _cacache. Disable scripts and isolate configuration, logs and HOME.
- Homebrew: manifest regular downloads older than 24 hours. Do not run generic brew cleanup.
- Chrome: only Cache and Code Cache under recognized cache profiles. Protect profile data and temporary app bundles.
- Xcode: only DerivedData project Build/Intermediates.noindex and Logs. Preserve Build/Products, installed applications, source and rollback artifacts.
- Docker: the daemon default build-cache predicate older than 24 hours, without all. Preserve images, containers and volumes. Keep execution BLOCKED without disposable-runtime qualification.

## Filesystem operations

Manifests use relative names and exact inode, device, owner, mode, size and modification time.
Skip new, changed, linked and locked files. Preserve lock files and directories. Require regular files owned by the current user.
Do not follow symlinks or cross filesystems. Pin parent directories before unlink and revalidate entries.
Use a 24-hour minimum age for manifest adapters. This limits the first release to stale files.
Plans are bounded to 100000 manifest entries and 64 MiB. Exceeding a bound blocks the target without deleting anything.

## Consumers and results

Do not stop applications or sessions. Positive Chrome/Xcode process observations block those categories.
Obtain available open-file observations with bounded lsof output. An unavailable required observation blocks the operation.
These are limited observations, not proof of global inactivity. A new reader can appear between observation and unlink.
Native manager protocols and manifest best-effort behavior are separate from the strict contract.
Report command status, partial changes, per-file outcomes and filesystem observations separately. No reclaim guarantee or summed Docker/APFS claim.

## Acceptance and exclusions

Use only private fixture caches for destructive tests. Never clean workstation caches, stop sessions, publish changes or install tools.
Keep uv out of execution because external symlink consumers remain unresolved.
Preserve Linux behavior. macOS application layouts require platform qualification.
TRIZ: ordinary engineering for adapters. No universal visibility guarantee is claimed.
