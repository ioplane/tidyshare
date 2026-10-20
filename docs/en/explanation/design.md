# Design

## Pipeline

```mermaid
flowchart LR
    walk[Walk tree, lstat] --> mime[MIME by content]
    mime --> size[Group by size]
    size --> sample[Hash head and tail 64 KiB]
    sample --> full[Hash whole file]
    full --> plan[dups.tsv]
    walk --> cand[Candidates]
    plan --> cand
    mime --> img[Image archives]
    img --> cand
    plan -. reviewed .-> apply[apply]
```

`scan` reads input files and writes reports under `-out`; that directory is
excluded from the walk. `apply` changes files from a reviewed plan and writes
its operation manifest under `-quarantine`.

## Duplicate detection

Candidates share a size of at least `-min-size`. A cheap sample (first and last
64 KiB plus the size) removes most non-duplicates before the full xxh3-128
hash. The hash identifies candidates; it is not trusted for destruction. In
`dedupe` mode the kernel compares the bytes again.

Hardlinks (same device and inode) are one file. Files under `.git/` and files
modified within `-settle` are never candidates.

## Why `apply` does not delete

| Risk | Handling |
| --- | --- |
| Software refers to a duplicate's path | `dedupe` keeps every path |
| The file changed after the scan | `apply` compares size and mtime and skips |
| Hash collision | `dedupe` uses the kernel's byte comparison |
| Wrong copy kept | the plan is reviewed text; `quarantine` is reversible by moving back |
| Free space during the operation | both modes work on one filesystem, no copy is made |

`dedupe` uses `FIDEDUPERANGE`. Deleting one of two extent-sharing files frees
no space until the other is gone as well.
Replacing two independently allocated identical extents with one shared
extent can free space immediately. Logical duplicate totals do not measure
that gain; already shared extents and sparse files can reduce it to zero.

## Tiers

Tier 1 is content that exists elsewhere or has junk names. Tier 2 needs a
human check: image archives (local storage is not a backup) and caches. Tier 3
is a decision about age or regenerability. Cold vendor media is a library, not
garbage: tier 3 lists it for visibility only.

## Limits

- The walk is single-threaded; hashing and MIME detection run in parallel.
- MIME uses the first 3 KiB of a file.
- Image matching compares image IDs; layers are not compared across images.
- Failed requested image stores are explicitly unavailable; they cannot
  establish that an unmatched archive is absent.
- Apply requires reviewed, quiet files and parent directories; a scan is not
  a filesystem snapshot or a lock against concurrent writers.
- Windows and macOS are not supported.
