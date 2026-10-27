# Workstation modernization and validation

This change adds scoped workstation inventory and reviewed managed cleanup for Linux and macOS. It keeps one Go CLI and local stdio MCP server. Docker and OrbStack inventory remains read-only.

## Scope

- Filesystem allocation, developer caches, Git trees and optional container runtime observations.
- Reviewed managed cleanup with exact plan bytes, digest, selected targets and current identities. MCP execution requires startup opt-in and a per-call request.
- Owned native process groups. The leader remains unreaped until the final destructive signal. After reap, only signal0 observation is allowed.
- Ownership, stop and I/O uncertainty preserve scratch roots and stop the remaining batch.
- Report write/Flush/Close errors propagate. YARA failures retain valid partial hits and private diagnostics. Public MCP scan fields remain unchanged.
- Shared CLI/MCP formats normalization and Podman parse errors without userinfo.
- Four release binaries plus a version-matched portable skill bundle. The bundle contains six operational/developer workflows and CLI/Codex/Claude examples. Installation is manual.

Go1.27.3 and the existing module stack remain in use. Darwin process observation uses public SDK waitid through cgo. Darwin without cgo refuses native execution before Start.

## Local validation

All133 product/delivery file hashes match the locally qualified implementation. This publication checkout omits private machine logs and internal execution records. Local results and GitHub CI results are separate evidence.

| Context | PASS | SKIP | FAIL | Qualification |
| --- | ---: | ---: | ---: | --- |
| macOS ARM64 | 335 | 9 | 0 | Native race/shuffle/coverage and actual release CLI/stdio MCP |
| Linux ARM64 on OrbStack | 431 | 13 | 0 | Native race, read-only network-isolated fixture container |
| Oracle Linux10.2 AMD64 | 434 | 10 | 0 | Unprivileged precompiled suite, SELinux Enforcing, private fixture namespace |
| Darwin AMD64 | — | — | — | CGO1 cross-build and build-info verification only |

Counts include parent/subtest outcomes. Skips remain gaps. The Oracle Linux domain was unconfined_service_t. Other domains remain unqualified. Namespaces were used only for disposable fixtures. They do not justify hiding real host consumers.

CLI and SDK stdio-MCP scenarios passed with ten fresh inputs on each of macOS and the raw Oracle Linux host. They covered formats, scopes, inventory, plan/dry-run and denied execution with a preserved sentinel. Inventory also completed against an actual OrbStack daemon.

Module, format, vet, lint and packager checks passed locally. GoReleaser2.18.3 verified complete snapshot, synthetic tag and prerelease asset sets. Four binaries and the bundle share checksums and build identity. Three platforms have native version receipts. Darwin AMD64 has build evidence only.

An independent Python check verified bundle paths, types, modes, ownership, VERSION and manifest hashes. Repeated canonical builds produced identical bytes. Archive mtimes use epoch0. Manifest and receipts record the full source commit.

With -trimpath, Go omits linker flags from build info. Digest-bound receipts therefore record the common build inputs. Native jobs also record actual version output. Foreign receipts do not establish runtime validation. The local host builder disabled module access but did not isolate OS networking. The CI offline/no-token container remains configured.

Govulncheck1.9.0 returned GO-2026-5932 for x/crypto v0.58.0 as a module-only trace, without a package/function trace. Reachable-symbol findings:0. Database timestamp:2026-10-21T00:27:29Z. This does not establish a general security clearance.

## Review and limits

The independent final review covered the modernization delta:0 Critical,0 Important,1 Minor. This PR also contains the earlier workstation and managed-cleanup changes for full product review.

Deferred Minor: build-ignore packager tests run explicitly in release CI, while package-wide PR tests skip them. Add the existing explicit script test command to PR CI in a follow-up.

Strict Darwin UNKNOWN/BLOCKED and managed LIMITED remain separate contracts. Incomplete host process or mount observation must keep cleanup blocked. PGID ownership does not establish arbitrary escaped-session containment or global inactivity.

Native Darwin AMD64 runtime, other SELinux domains, installed client discovery and formal legacy equivalence remain unverified. No release or global installation forms part of this draft PR.
