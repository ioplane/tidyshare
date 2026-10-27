# Release verification

Record the source commit and canonical version. A release tag supplies the version without its leading `v`. An untagged build uses `0.0.0-dev.<fullSHA>`. Binary version output uses a leading `v`. Verify that the full commit and version match all four Linux and Darwin binaries and the bundle.

Run the release workflow in dry-run mode before publication. Inspect the staged archive, manifest, required workflows, relative links and checksum list. Verify the four binaries and bundle against the same checksum file. Run native binary smoke tests on the relevant platforms. Record unavailable runners as gaps. A dry run does not publish or produce release attestations.

Publication needs separate authorization. After an authorized release, download the assets and verify their checksums and attestations against the tag and commit. Read back the published release and asset set. A local build or successful workflow alone does not prove publication.

For a local snapshot builder check, prepare the same input pair first:

```sh
set -eu
TIDYSHARE_SOURCE_COMMIT=$(git rev-parse HEAD)
TIDYSHARE_RELEASE_VERSION=$(GOTOOLCHAIN=local go run scripts/agent_bundle.go -ref-type branch -ref local -commit "$TIDYSHARE_SOURCE_COMMIT")
TIDYSHARE_LDFLAGS=$(GOTOOLCHAIN=local go run scripts/agent_bundle.go -print-ldflags -version "$TIDYSHARE_RELEASE_VERSION" -commit "$TIDYSHARE_SOURCE_COMMIT")
export TIDYSHARE_SOURCE_COMMIT TIDYSHARE_RELEASE_VERSION TIDYSHARE_LDFLAGS
goreleaser check
goreleaser release --snapshot --skip=publish
```

Use the workflow-pinned GoReleaser version. This local command checks the Linux
builder. The full dry run also needs Darwin outputs, the bundle, receipts and
common checksums. A build receipt for a foreign architecture is not a runtime
check. Keep these two evidence states separate.
