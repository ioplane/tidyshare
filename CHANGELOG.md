# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
