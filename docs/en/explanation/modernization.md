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

## Original qualification (2026-10-27)

The original publication commit, `7605316`, contained 133 product/delivery files that matched the qualified implementation by hash. This section records that original qualification. The checkout omits private machine logs and internal execution records. Local results and GitHub CI results are separate evidence.

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

## Review fixes and qualification (2026-11-09)

The review listed 60 SonarCloud findings, including 41 cognitive complexity findings. The fixes retain the CLI/MCP schemas, exit codes and execution gates.

- Verification containers use an init process to reap orphaned descendants. The process owner still requires proof that its group stopped. The default user is `nobody:nogroup`. Only the formatter and the explicit root-owned inventory fixture tests use root.
- Release staging archives retain private permissions. Archive members retain the existing metadata and manifest contract.
- The flagged functions split collection, validation, execution and reporting into separate steps. A local execution state retains the common action limit and journal callbacks without long parameter lists.
- PR CI explicitly runs the standalone bundle tests on Linux and both Darwin architectures. The formatting gate also retains formatter errors. Checks with invalid syntax and unformatted code fail, while formatted code passes.
- Both container stages retain the original image digest. Repeated literals and the reported style issues use the existing code conventions.
- Scan flags fill caller-owned options. This resolves two G702 findings introduced by the extracted configuration return value. Executable selection and separate argv remain unchanged.
- `github.com/klauspost/compress` changes from `v1.19.1` to `v1.20.2` for [GO-2026-6865](https://pkg.go.dev/vuln/GO-2026-6865).

Local qualification uses source [`0d50c8c`](https://github.com/ioplane/tidyshare/commit/0d50c8c4b845366a827c8bafbc8ca48f20c13119).

| Context | PASS | SKIP | FAIL | Qualification |
| --- | ---: | ---: | ---: | --- |
| macOS ARM64 | 335 | 9 | 0 | Native race/shuffle/coverage |
| Linux ARM64 on OrbStack | 431 | 13 | 0 | Compose race/shuffle/coverage with init, as nobody |
| Linux root-owned inventory fixtures | 10 | 0 | 0 | Explicit root lane restores privileged test coverage |
| Linux standalone bundle tests | 40 | 0 | 0 | Runs as nobody |

Counts include parent/subtest outcomes. Skips remain gaps. Actual CLI/SDK stdio-MCP checks passed with ten new inputs per platform. The server refused execution without startup opt-in. The synthetic sentinel remained intact. Actual OrbStack inventory also completed in read-only mode.

Uncapped gocognit checks covered 41 listed functions and 126 new function declarations across Darwin, Linux and the standalone bundle tool. None exceeded 15. The existing unflagged findings remain outside this review-fix scope. The update does not change lint rules or add Sonar exclusions.

Snapshot, tag and prerelease scenarios passed with four binaries, the bundle, receipts and common checksums. Repeated bundles had identical bytes. Local native receipts cover Darwin ARM64 and Linux ARM64. Local AMD64 receipts establish builds only.

The independent reviews found a lint regression and the formatting gate error. Scoped re-review checked the fixes for lint, complexity, the dependency, execution state, container permissions and formatting. It found no new Critical or Important findings.

Govulncheck 1.9.0 completed a symbol scan with database timestamp 2026-11-05T23:49:57Z. No package/function findings remained. Four module-only findings remain, without call traces. This does not establish a general security clearance.

GitHub Actions and SonarCloud checks apply to the exact pushed PR head. Their current results are separate from these local checks. See [PR20 checks](https://github.com/ioplane/tidyshare/pull/20/checks).

## Review and limits

The independent final review covered the modernization delta:0 Critical,0 Important,1 Minor. This PR also contains the earlier workstation and managed-cleanup changes for full product review.

The original deferred Minor concerned build-ignore packager tests. The review fixes add their explicit command to PR CI.

Strict Darwin UNKNOWN/BLOCKED and managed LIMITED remain separate contracts. Incomplete host process or mount observation must keep cleanup blocked. PGID ownership does not establish arbitrary escaped-session containment or global inactivity.

Local release checks do not provide native AMD64 receipts. PR CI runs native Darwin tests and builds on both architectures. New Oracle Linux/SELinux qualification, installed client discovery and formal legacy equivalence remain unverified. No release or global installation forms part of this draft PR.
