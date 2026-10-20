# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Preserve native Podman image names, distinguish untagged images, and keep
  unknown container counts out of the tagged-without-containers total.
- Report unavailable image stores explicitly and prevent incomplete queries
  from classifying an unmatched image archive as absent.
- Validate the complete duplicate plan before applying it, keep dry runs
  free of writes, and refuse quarantine destination replacement.
- Normalize scan roots to absolute paths and reject invalid roots or
  numeric bounds before creating report files.

### Added

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
