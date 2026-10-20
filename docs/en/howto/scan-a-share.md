# Scan a share

Run a read-only scan and read the reports.

```bash
tidyshare scan -root /srv/share -out /var/tmp/tidyshare-report
```

Keep `-out` on a different filesystem when the share is nearly full. Reports
list every file path: store them like the data they describe.

The scan skips files modified within `-settle` (default one hour) for
duplicate detection, because they may still be written.

## Read the result

1. `report.md`: totals, categories, MIME types, top-level areas, the largest
   directories and cold directories.
2. `candidates.md`: what can be removed, in three tiers. Each file is counted
   once, in its lowest tier.
3. `dups.tsv`: the duplicate plan. Edit it before `apply`: change `keep` and
   `dup` where the automatic choice of the kept copy is wrong.

## Tune the scan

```bash
tidyshare scan -root /srv/share -out ./report \
  -min-size 4194304 -stale-days 365 -skip /srv/share/.cache,/srv/share/tmp
```

The scan hashes only files that share a size with another file, so a tree with
few duplicates is read once for metadata and for the first and last 64 KiB.
