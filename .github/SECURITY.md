# Security policy

## Reporting a vulnerability

Report vulnerabilities through
[GitHub private vulnerability reporting](https://github.com/ioplane/tidyshare/security/advisories/new).
Do not open a public issue, pull request or discussion before coordinated
disclosure.

Include the affected version or commit, the command line, prerequisites, the
observable impact and a minimal reproducer on a disposable directory. Do not
include real file listings, credentials or host identifiers.

Maintainers target acknowledgment within 3 business days, an initial
assessment within 7 calendar days and a remediation or disclosure plan within
30 calendar days. The reporter receives a status update at least every 14
days.

## Supported versions

Before `v1.0.0`, only the newest released minor line receives security fixes.
After `v1.0.0`, the current major release and the one before it receive fixes
when technically practical.

## Security properties

Reports are especially valuable when tidyshare can:

- modify, move or delete a file during `scan`, which is read-only by contract;
- act in `apply` without `-yes`, or outside the files named by the plan;
- follow a symbolic link or cross a filesystem boundary during `apply`;
- replace a file whose content or metadata changed after the scan;
- lose data while sharing extents (`apply -mode dedupe`);
- be driven by a crafted `dups.tsv`, file name or rules file to run commands
  or write outside the report and quarantine directories;
- publish unverifiable release artifacts or expose release credentials.

Reports contain file paths. Treat the report directory as sensitive data.

## Release verification

Release binaries come with a SHA-256 checksum file and GitHub build
provenance. Verify them before running the binary with elevated privileges:

```bash
sha256sum --check tidyshare_<version>_checksums.txt
gh attestation verify tidyshare_<version>_linux_amd64 --repo ioplane/tidyshare
```

## Disclosure and credit

Maintainers coordinate disclosure dates and request CVEs when appropriate.
Reporter credit is included with consent.
