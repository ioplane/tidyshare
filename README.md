# tidyshare

[![CI](https://github.com/ioplane/tidyshare/actions/workflows/ci.yml/badge.svg)](https://github.com/ioplane/tidyshare/actions/workflows/ci.yml)
[![CodeQL](https://github.com/ioplane/tidyshare/actions/workflows/codeql.yml/badge.svg)](https://github.com/ioplane/tidyshare/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/ioplane/tidyshare/badge)](https://scorecard.dev/viewer/?uri=github.com/ioplane/tidyshare)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Scanner and controlled cleanup tool for large file shares. It categorises files,
finds byte-identical duplicates, ranks what is safe to remove, flags stale
trees and matches container image archives against local podman storage. A
separate `apply` step reclaims space from a reviewed duplicate plan without
deleting files. Explicit `cleanup` deletes reviewed redundant files or orphan
Podman images, with dry run as the default. A local MCP server exposes these
operations within configured filesystem and image-store boundaries.

## Features

- **Scan** one tree in one pass: size, mtime, extension category and content
  MIME (magic bytes, so renamed files are still typed).
- **Duplicates** found by size, then head/tail sample, then full xxh3-128
  hash. Hardlinks count as one file.
- **Deletion candidates** in three tiers: safe (duplicates, junk), review
  (image archives already in storage, caches), decide (regenerable output,
  trees untouched for N days), per directory and per file.
- **Container images**: lists podman images and matches docker-archive and
  oci-archive files by image ID.
- **YARA**: optional scan of binaries, installers and packages with the
  classic `yara` CLI.
- **Apply** a reviewed plan: dry run by default; either move duplicates into a
  quarantine or share their extents with the kept copy (XFS reflink, Btrfs).
- **Cleanup** reviewed duplicate files or unnamed orphan images: revalidate
  every target and use `-yes` only after reviewing the dry-run report.
- **Reports** in selectable JSON, JSONL and Markdown formats (`-formats`).
- **Developer storage**: finds language caches and Git trees; clears selected
  caches from a reviewed plan with busy-process and source-data guards.
- **MCP** over local stdio, scoped to an explicit root and image store;
  deletion requires startup opt-in and an explicit tool request.

## Workstation support

The source tree adds native macOS scans, Git/cache inventory, quarantine,
reviewed duplicate deletion and local Docker inventory. Darwin cache execution
is not qualified because complete process visibility is not established.
Published `v0.1.0` assets remain Linux-only.
See [the workstation workflow](docs/en/howto/workstation.md).

## Install

Download [v0.1.0](https://github.com/ioplane/tidyshare/releases/tag/v0.1.0),
verify it, and put the binary on `PATH`. Static Linux amd64 and arm64
executables are available; neither requires CGO or a local Podman installation
for file-only operations.

```bash
gh release download v0.1.0 --repo ioplane/tidyshare
sha256sum --check tidyshare_v0.1.0_checksums.txt
gh attestation verify tidyshare_v0.1.0_linux_amd64 --repo ioplane/tidyshare
install -m 0755 tidyshare_v0.1.0_linux_amd64 /usr/local/bin/tidyshare
```

Use the `linux_arm64` asset on ARM64 systems. See the
[release process](docs/en/explanation/releases.md) for provenance verification
and Actions snapshots. Or build this release from source with Go:

```bash
CGO_ENABLED=0 go install -tags remote,exclude_graphdriver_btrfs,containers_image_openpgp github.com/ioplane/tidyshare/cmd/tidyshare@v0.1.0
```

## Agent bundle

The release pipeline builds a portable agent bundle beside the CLI binaries.
Use a bundle and binary from the same version and source commit. Verify release
checksums and attestations before use. Copy the whole extracted bundle into a
client-specific skill directory. See [the bundle README](agent-bundle/README.md)
for manual setup and workflow links. The bundle does not install itself or
authorize cleanup. Published v0.1.0 assets do not include this bundle.

## Quick start

```bash
tidyshare scan -root /srv/share -out ./report -images=false
less report/report.md report/candidates.md
```

Review `report/dups.tsv`, then try a dry run and a one-file canary:

```bash
tidyshare apply -plan report/dups.tsv -mode dedupe -quarantine /srv/share/.tidyshare
tidyshare apply -plan report/dups.tsv -mode dedupe -quarantine /srv/share/.tidyshare -limit 1 -yes
```

Delete reviewed duplicate copies or prepare conservative image cleanup:

```bash
tidyshare cleanup -kind duplicates -plan report/dups.tsv -out ./cleanup-review -formats json,jsonl,md
tidyshare cleanup -kind duplicates -plan report/dups.tsv -out ./cleanup-executed -formats json,jsonl,md -limit 1 -yes
tidyshare images-plan -owner root -out ./image-review -formats json,jsonl,md
tidyshare cleanup -kind images -plan image-review/image-plan.json -out ./image-cleanup -formats json,jsonl,md
tidyshare mcp -root /srv/share -out /var/tmp/tidyshare-reports
```

Find developer caches and Git trees, then review a cache cleanup dry run:

```bash
tidyshare discover -root /srv/developers -out ./developer-review -formats json,jsonl,md
tidyshare cleanup -kind caches -plan developer-review/cache-plan.json -out ./cache-review
```

See [language cache cleanup](docs/en/howto/clean-language-caches.md) for the
supported managers, protected storage and execution workflow.

## Requirements

- Linux, or a native macOS source build for the supported workstation operations.
  Darwin cache execution remains unqualified. `scan` reads trees the user can read.
- `apply -mode dedupe`: a filesystem with `FIDEDUPERANGE` support (XFS with
  `reflink=1`, Btrfs).
- Optional: a running local Podman API service for image matching, `yara`
  4.x for rule scans. Inventory uses the official Podman Go SDK.

## Safety

`scan` reads the selected files, stages reports in a private temporary directory
and publishes them to `-out`, which is excluded from the scan. `apply` does nothing without
`-yes`, skips any file whose size or mtime changed after the scan, refuses to
cross a filesystem boundary and never deletes a file. Use reviewed plans
while the selected files and their parent directories are quiet. See
[Design](docs/en/explanation/design.md).
`cleanup` deletes only targets from the reviewed plan after revalidation.
Keep source files, parent directories and the Podman store quiet during cleanup.
Reports use new destinations and refuse existing files or symlink components.
An image plan is not a backup; retain a verified recovery source before deletion.

## Documentation

| Topic | Document |
| --- | --- |
| Scan a share | [docs/en/howto/scan-a-share.md](docs/en/howto/scan-a-share.md) |
| Find developer storage | [docs/en/howto/find-developer-storage.md](docs/en/howto/find-developer-storage.md) |
| Clean language caches | [docs/en/howto/clean-language-caches.md](docs/en/howto/clean-language-caches.md) |
| Reclaim space | [docs/en/howto/reclaim-space.md](docs/en/howto/reclaim-space.md) |
| Clean orphan images | [docs/en/howto/clean-images.md](docs/en/howto/clean-images.md) |
| Inspect a macOS workstation | [docs/en/howto/workstation.md](docs/en/howto/workstation.md) |
| Use MCP | [docs/en/howto/use-mcp.md](docs/en/howto/use-mcp.md) |
| Match container images | [docs/en/howto/match-container-images.md](docs/en/howto/match-container-images.md) |
| Scan with YARA | [docs/en/howto/scan-with-yara.md](docs/en/howto/scan-with-yara.md) |
| CLI reference | [docs/en/reference/cli.md](docs/en/reference/cli.md) |
| Report files | [docs/en/reference/reports.md](docs/en/reference/reports.md) |
| Design and limits | [docs/en/explanation/design.md](docs/en/explanation/design.md) |
| Releases and artifact verification | [docs/en/explanation/releases.md](docs/en/explanation/releases.md) |

## Project

[Contributing](.github/CONTRIBUTING.md) ·
[Code of Conduct](.github/CODE_OF_CONDUCT.md) ·
[Security](.github/SECURITY.md) ·
[Support](.github/SUPPORT.md) ·
[Governance](.github/GOVERNANCE.md) ·
[Changelog](CHANGELOG.md) ·
[License](LICENSE)
