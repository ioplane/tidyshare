# Clean language caches

Discover caches beneath an explicit root, review the generated plan, then run a
one-target canary. Stop builds, package managers and development sessions that
use these caches; keep the directories quiet until cleanup finishes.

```bash
tidyshare discover -root /srv/developers -out ./developer-review -formats json,jsonl,md
tidyshare cleanup -kind caches -plan developer-review/cache-plan.json -out ./cache-dry-run
tidyshare cleanup -kind caches -plan developer-review/cache-plan.json -out ./cache-canary -limit 1 -yes
```

Discovery measures logical regular-file bytes and regular-file counts without
following symlinks. These values are not a prediction of physical free space:
hardlinks, shared extents and concurrent activity affect filesystem accounting.

| Kind | Recognized layout beneath the selected root | Cleanup |
| --- | --- | --- |
| `go_build` | `.cache/go-build` | `go clean -cache` |
| `go_modules` | `go/pkg/mod` | `go clean -modcache` |
| `pip` | `.cache/pip` | `pip cache purge` |
| `uv` | `.cache/uv` | `uv cache clean --force` after the busy guard |
| `npm` | `.npm/_cacache` | `npm cache clean --force` with the containing cache root |
| `pnpm` | `.local/share/pnpm/store` | `pnpm store prune`, unused packages only |
| `yarn` | `.cache/yarn` | Discovery only; execution reports unsupported |
| `yarn_berry` | `.yarn/berry/cache` | Discovery only; execution reports unsupported |
| `cargo` | `.cargo/registry/cache` | Descriptor-relative deletion of `.crate` archives only |
| `gradle` | `.gradle/caches` | Descriptor-relative deletion after source/configuration guards |
| `nuget_packages` | `.nuget/packages` | `dotnet nuget locals global-packages --clear` |
| `nuget_http` | `.local/share/NuGet/v3-cache` | `dotnet nuget locals http-cache --clear` |
| `maven` | `.m2/repository` | Protected; may contain unpublished local packages |

Yarn and Maven entries carry `cleanup=false` and a reason; they are omitted from
the generated cleanup plan.

Current absolute environment settings can qualify custom cache locations:
`GOCACHE`, `GOMODCACHE`, each `GOPATH` entry plus `pkg/mod`, `PIP_CACHE_DIR`,
`UV_CACHE_DIR`, `NPM_CONFIG_CACHE`/`npm_config_cache` plus `_cacache`,
`CARGO_HOME` plus `registry/cache`, `GRADLE_USER_HOME` plus `caches`,
`NUGET_PACKAGES`, `NUGET_HTTP_CACHE_PATH`, and the corresponding
`XDG_CACHE_HOME`/`XDG_DATA_HOME` layouts above. Custom roots need positive cache
markers during discovery; unknown layouts are omitted. Package-marker discovery
examines bounded directory batches, so unusual custom trees can be omitted.
Configuration files are not executed to discover paths. Preserve the same
settings when reviewing or replaying a plan. The filesystem root, current home,
manager home directories, virtual environments, `node_modules`, Rust toolchains
and project `.yarn/cache` zero-install archives are not disposable cache roots.

The complete plan is validated before execution. Every target is checked against
its recorded device, inode and owner. Cleanup refuses symlink path components,
nested mounts (including bind mounts), open files, process working directories
and mapped payloads. Raw process symlink paths remain literal; escaped proc
text paths are decoded separately, preserving literal backslashes and names
ending in ` (deleted)`. An uninspectable process prevents cleanup. On shared
hosts, an unprivileged account may need elevated process-inspection privileges;
an inaccessible process can still use an owner-private cache. Review skipped
results instead of treating them as empty or safe caches. Direct cleanup
refuses symlinks, special files, hardlinked payloads, source files, Git metadata
and configuration, and preserves the cache root. Bare Git repositories are
recognized through pinned, non-following `HEAD`/`objects`/`refs` metadata checks
at every directory before deletion. Cache roots and arbitrary nested bare
repositories are protected. A bare repository at a canonical cache root is
left to Git discovery instead of entering the cache plan. Only native-owned Go `cache/vcs/<64-hex>` and uv
`git-v0/db/<hex>` or `git-v1/db/<hex>` databases may pass this guard. Native uv cleanup may remove
cache-owned extracted source trees and Git clones; this is not generic Git
repository cleanup. Its remover unlinks nested symlinks without following them.

pnpm prunes only the installed manager's current store namespace; older store
formats may remain. New manager metadata can offset removed payload bytes, so
a successful prune can report no measured net decrease.

Native commands run without a shell, in a private working directory with an
isolated home/configuration environment and bounded output. npm and pnpm use
distinct empty private user/global configuration files; pointing both scopes
at the same file is invalid for npm. Missing tools and
unsupported Yarn cleaners are explicitly skipped; tools are never installed.
Yarn execution remains unsupported because project rc/plugin and executable
selection need independent qualification. Native command failures return an
error with a partial report. Native commands own a process group; cancellation
kills that group, and manager exit also terminates descendants. Pipe draining is
bounded. Cancellation stops subsequent targets. Once deletion starts, an
`interrupted` result means partial changes are possible and the reported totals
are incomplete; inspect the cache and generate a fresh plan before continuing.

Dry run starts no package manager and changes no cache data. `-yes` enables
execution; `-limit` bounds the number of reviewed targets considered. Results
include `would_clear`, `cleared`, `already_empty`, `absent`, `skipped` and
`interrupted`.
`changed` counts cache targets whose measured contents decreased;
`logical_bytes` is the observed logical-byte decrease. Retained package-manager
metadata and referenced pnpm packages are not evidence of failed reclamation.
Repeat cleanup reports zero changes for an empty or absent cache. Plans do not
prevent idle producers from starting between checks: the quiet-directory
requirement remains necessary. uv's `--force` bypasses its native lock and is
used only after the process/busy checks; do not use cleanup alongside uv sessions.

Sources:

- [Go clean](https://github.com/golang/go/blob/master/src/cmd/go/internal/clean/clean.go)
  [module cache removal](https://github.com/golang/go/blob/master/src/cmd/go/internal/modfetch/fetch.go),
  and [command cancellation and pipe bounds](https://github.com/golang/go/blob/master/src/os/exec/exec.go).
- [pip caching](https://pip.pypa.io/en/stable/topics/caching/).
- [uv cache safety](https://github.com/astral-sh/uv/blob/main/docs/concepts/cache.md)
  and [symlink-safe removal](https://github.com/astral-sh/uv/blob/main/crates/uv-cache/src/removal.rs).
- [npm cache](https://docs.npmjs.com/cli/v11/commands/npm-cache/)
  and [pnpm store](https://pnpm.io/cli/store).
- [Yarn cache command](https://github.com/yarnpkg/berry/blob/master/packages/plugin-essentials/sources/commands/cache/clean.ts).
- [Cargo home](https://doc.rust-lang.org/cargo/guide/cargo-home.html)
  and [Gradle directories](https://docs.gradle.org/current/userguide/directory_layout.html).
- [NuGet local caches](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-nuget-locals).
