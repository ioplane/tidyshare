# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Report publication refuses existing output files instead of replacing them.
  Use a new output directory for each run (breaking change).
- Query Podman through the official Go SDK and accept explicit owner-bound
  Unix sockets with `-image-sockets`, including nonstandard rootless runtimes.
  Scanning never starts or recovers an API service. The deprecated `-engine`
  flag accepts only `podman`; custom engine binaries are no longer executed.
- Build the remote SDK client with `remote,exclude_graphdriver_btrfs,containers_image_openpgp` tags to
  retain CGO-free release binaries.

### Fixed

- Preserve image inventory and archive matches when optional Podman disk-usage
  statistics are unavailable.
- Preserve native Podman image names, distinguish untagged images, and keep
  unknown container counts out of the tagged-without-containers total.
- Report unavailable image stores explicitly and prevent incomplete queries
  from classifying an unmatched image archive as absent.
- Validate the complete duplicate plan before applying it, keep dry runs
  free of writes, and refuse quarantine destination replacement.
- Normalize scan roots to absolute paths and reject invalid roots or
  numeric bounds before creating report files.
- Keep directory summary names relative when scanning the filesystem root.

### Added

- `cleanup -kind duplicates` deletes reviewed byte-identical copies while
  preserving the keeper; `-yes` is required to execute instead of dry run.
- `images-plan` and `cleanup -kind images` review and remove exact dangling
  Podman image IDs with fresh store and container-reference checks.
- Selectable `-formats json,jsonl,md` reports for scan and cleanup operations,
  with structured summaries and per-target outcomes.
- Local stdio MCP tools for scanning, image planning and cleanup within
  startup-bound roots and image sockets. Cleanup execution requires both
  `-allow-cleanup` and the tool's `execute` argument.
- `tidyshare scan`: read-only scan of a directory tree. Categorises files by
  extension and by content (MIME), finds byte-identical duplicates with a
  size, head/tail and full xxh3-128 cascade, flags junk and cold files, and
  writes `report.md`, `catalog.jsonl`, `dups.tsv`, `stale.tsv`.
- Tiered deletion-candidate report (`candidates.md`, `candidates.tsv`):
  duplicate copies, wholly redundant directories, regenerable directories and
  trees untouched for `-stale-days`.
- Container image listing from podman storage and matching of docker-archive
  and oci-archive files against it by image ID (`images.md`, `images.tsv`).
- Optional scan of selected categories with the classic `yara` CLI
  (`-yara-rules`, `yara.tsv`).
- `tidyshare apply`: dry run by default. `-mode quarantine` moves duplicates
  into a same-filesystem directory; `-mode dedupe` shares their extents with
  the kept copy through `FIDEDUPERANGE`. Both re-check size and mtime and
  write a manifest.
- `tidyshare version`.
