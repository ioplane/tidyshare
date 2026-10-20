# tidyshare

[![CI](https://github.com/ioplane/tidyshare/actions/workflows/ci.yml/badge.svg)](https://github.com/ioplane/tidyshare/actions/workflows/ci.yml)
[![CodeQL](https://github.com/ioplane/tidyshare/actions/workflows/codeql.yml/badge.svg)](https://github.com/ioplane/tidyshare/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/ioplane/tidyshare/badge)](https://scorecard.dev/viewer/?uri=github.com/ioplane/tidyshare)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Read-only scanner and reviewer for large file shares. It categorises files,
finds byte-identical duplicates, ranks what is safe to remove, flags stale
trees and matches container image archives against local podman storage. A
separate `apply` step reclaims space from a reviewed duplicate plan without
deleting files.

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

## Install

Download a release binary, verify it, and put it on `PATH`:

```bash
sha256sum --check tidyshare_<version>_checksums.txt
gh attestation verify tidyshare_<version>_linux_amd64 --repo ioplane/tidyshare
install -m 0755 tidyshare_<version>_linux_amd64 /usr/local/bin/tidyshare
```

Or build from source with Go:

```bash
go install github.com/ioplane/tidyshare/cmd/tidyshare@latest
```

## Quick start

```bash
tidyshare scan -root /srv/share -out ./report
less report/report.md report/candidates.md
```

Review `report/dups.tsv`, then try a dry run and a one-file canary:

```bash
tidyshare apply -plan report/dups.tsv -mode dedupe -quarantine /srv/share/.tidyshare
tidyshare apply -plan report/dups.tsv -mode dedupe -quarantine /srv/share/.tidyshare -limit 1 -yes
```

## Requirements

- Linux. `scan` also reads from any tree the user can read.
- `apply -mode dedupe`: a filesystem with `FIDEDUPERANGE` support (XFS with
  `reflink=1`, Btrfs).
- Optional: `podman` for image matching, `yara` 4.x for rule scans.

## Safety

`scan` reads the selected files and writes reports only to `-out`, which is
excluded from the scan. `apply` does nothing without
`-yes`, skips any file whose size or mtime changed after the scan, refuses to
cross a filesystem boundary and never deletes a file. Use reviewed plans
while the selected files and their parent directories are quiet. See
[Design](docs/en/explanation/design.md).

## Documentation

| Topic | Document |
| --- | --- |
| Scan a share | [docs/en/howto/scan-a-share.md](docs/en/howto/scan-a-share.md) |
| Reclaim space | [docs/en/howto/reclaim-space.md](docs/en/howto/reclaim-space.md) |
| Match container images | [docs/en/howto/match-container-images.md](docs/en/howto/match-container-images.md) |
| Scan with YARA | [docs/en/howto/scan-with-yara.md](docs/en/howto/scan-with-yara.md) |
| CLI reference | [docs/en/reference/cli.md](docs/en/reference/cli.md) |
| Report files | [docs/en/reference/reports.md](docs/en/reference/reports.md) |
| Design and limits | [docs/en/explanation/design.md](docs/en/explanation/design.md) |

## Project

[Contributing](.github/CONTRIBUTING.md) ·
[Code of Conduct](.github/CODE_OF_CONDUCT.md) ·
[Security](.github/SECURITY.md) ·
[Support](.github/SUPPORT.md) ·
[Governance](.github/GOVERNANCE.md) ·
[Changelog](CHANGELOG.md) ·
[License](LICENSE)
