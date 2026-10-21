# Releases

The [Release workflow](../../../.github/workflows/release.yml) builds Linux
amd64 and arm64 binaries through [GoReleaser](https://goreleaser.com/) inside
the digest-pinned Podman development image. Production binaries use
`CGO_ENABLED=0`, the remote Podman build tags, trimmed source paths, the
release version and the full source commit. GoReleaser runs without network
access or credentials, with its SCM publisher disabled. GitHub CLI handles
release publication. No container image is published.

## Release v0.1.0

[v0.1.0](https://github.com/ioplane/tidyshare/releases/tag/v0.1.0) is the first
public release, from source commit
[`dd679d1af75931809ab08ba1fe8450c49d11ab41`](https://github.com/ioplane/tidyshare/commit/dd679d1af75931809ab08ba1fe8450c49d11ab41).

| Asset | Purpose |
| --- | --- |
| `tidyshare_v0.1.0_linux_amd64` | Static executable for Linux x86-64 |
| `tidyshare_v0.1.0_linux_arm64` | Static executable for Linux ARM64 |
| `tidyshare_v0.1.0_checksums.txt` | SHA256 of both executables |

All three assets have verified provenance from the
[Release run](https://github.com/ioplane/tidyshare/actions/runs/39596074712).
The Actions artifact also contains `metadata.json` and `artifacts.json`; these
files are not GitHub Release assets. Verification covers both ELF architectures,
checksums, `CGO_ENABLED=0`, embedded version and source commit. The executable
smoke test covers amd64; ARM64 runtime validation is not recorded.

Verify an artifact against the release tag and source commit:

```bash
gh attestation verify tidyshare_v0.1.0_linux_amd64 \
  --repo ioplane/tidyshare \
  --signer-workflow ioplane/tidyshare/.github/workflows/release.yml \
  --source-ref refs/tags/v0.1.0 \
  --source-digest dd679d1af75931809ab08ba1fe8450c49d11ab41
```

Use the same verification for the ARM64 executable and checksum file.

## Verify without publishing

Run **Release** from the Actions tab with `dry_run=true` (the default), or:

```bash
gh workflow run release.yml --repo ioplane/tidyshare --ref main -f dry_run=true
```

The workflow runs the module, formatting, vet, lint, test, race and vulnerability
gates, validates `.goreleaser.yaml`, and builds a no-token snapshot. Download
`release-<commit>` from that run's Actions artifacts; retention is 14 days.
It includes both binaries, SHA256 checksums and GoReleaser metadata. Snapshots
do not create GitHub Releases or provenance attestations.

## Publish a version

Choose a SemVer tag such as `v<major>.<minor>.<patch>` or
`v<major>.<minor>.<patch>-rc.1` for a reviewed commit already merged into `main`.
Push that tag with an administrator account allowed by the repository's tag
rules. A tag push starts publication automatically. A manual run on an existing
tag publishes only with `dry_run=false`; a branch cannot publish.

GitHub CLI creates a draft release with raw binaries named
`tidyshare_<tag>_linux_amd64` and `tidyshare_<tag>_linux_arm64`, plus
`tidyshare_<tag>_checksums.txt`. Release notes use the existing
[GitHub categories](../../../.github/release.yml). Prerelease tags retain their
prerelease status. The workflow records build provenance, saves Actions
artifacts, then makes the draft public. An earlier failure leaves the release
unpublished. A retry refuses any existing release for the tag, including a
draft. Inspect the failure and explicitly remove an incomplete draft before
retrying; published releases and their assets are not replaced automatically.
Do not move or recreate a published release tag.

Download all three assets into the same directory, then verify the checksums
and the attestation before installation:

```bash
sha256sum --check tidyshare_<tag>_checksums.txt
gh attestation verify tidyshare_<tag>_linux_amd64 --repo ioplane/tidyshare
chmod +x tidyshare_<tag>_linux_amd64
./tidyshare_<tag>_linux_amd64 version
```

The checksum verifies downloaded bytes; the attestation binds those bytes to
the workflow and source repository. The ARM64 executable requires an ARM64
host for runtime validation.
