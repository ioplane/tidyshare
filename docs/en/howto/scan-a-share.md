# Scan a share

Run a read-only scan and read the reports.

```bash
tidyshare scan -root /srv/share -out /var/tmp/tidyshare-report -images=false
```

Keep `-out` on a different filesystem when the share is nearly full. Reports
list every file path: store them like the data they describe.
This file-only scan does not query Podman. Enable `-images` when a local API
service is available; see [Match container images](match-container-images.md).

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
  -images=false -min-size 4194304 -stale-days 365 -skip /srv/share/.cache,/srv/share/tmp
```

Duplicate detection groups eligible files by size before reading samples.
Files with unique sizes receive no duplicate hash. Equal-size candidates are
sampled at the first and last 64 KiB; only groups with equal samples receive
a full-file hash. MIME detection remains a separate content read.
