# Scan with YARA

`scan` can run the classic `yara` CLI (4.x, `--scan-list` support) over the
files of selected categories.

```bash
tidyshare scan -root /srv/share -out ./report -yara-rules ./rules
```

`-yara-rules` is a rules file or a directory of `.yar` and `.yara` files. For a
directory, tidyshare adds files one by one and skips any file that fails to
compile together with the ones accepted before it, with a warning.

Defaults: categories `binary,installer,package,firmware,dump`, files up to
512 MiB (`-yara-max-size`), 120 s per file. Results are in `yara.tsv`
(`rule`, `size`, `path`); the tool's stderr is in `yara.err`.

A match is a lead, not a verdict. Packer and heuristic rule sets match benign
software.

YARA-X (`yr`) is not driven by tidyshare: its command line documents no
equivalent of `--scan-list`.
