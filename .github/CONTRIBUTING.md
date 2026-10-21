# Contributing to tidyshare

tidyshare reads large file shares and, on request, rewrites file storage
(`apply`). Changes are accepted when their safety effect is explicit, tested
and reviewable.

By participating, you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md)
and to license contributions under [Apache-2.0](../LICENSE).

## Before opening a change

- Open an issue first for new behavior, CLI or report-format changes, and
  anything that touches `apply`.
- Report vulnerabilities privately; follow [SECURITY.md](SECURITY.md).
- Keep pull requests focused. Separate mechanical changes from behavior.
- Do not put real host names, internal paths, credentials or production file
  listings in fixtures, issues or documentation.

## Commits

Commits follow
[Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
with a declarative description:

```text
feat(scan): add a size histogram to report.md
fix(apply): skip a duplicate whose mtime changed after the scan
docs(howto): describe the canary run
```

Mark incompatible CLI, report-column or exit-code changes with `!` and a
`BREAKING CHANGE:` footer.

## Development environment

Formatting, analysis and tests run in the pinned image defined by the
[Containerfile](../Containerfile). Podman is the supported container engine.
The image sets `GOFLAGS` to build the official remote Podman client with
`remote,exclude_graphdriver_btrfs,containers_image_openpgp`; these tags keep production builds free of
CGO and local storage-engine dependencies. Use the same tags for direct Go
commands outside the image.

```bash
podman build --tag localhost/ioplane/tidyshare-dev:1.27.3 --file Containerfile .
podman compose run --rm format
podman compose run --rm modcheck
podman compose run --rm vet
podman compose run --rm lint
podman compose run --rm test
podman compose run --rm race
podman compose run --rm vuln
```

## Testing expectations

Behavior changes start with a failing test. Changes to `apply` need tests for
the refusal paths: a changed file, a different filesystem, a keeper that
disappeared, content that differs. No test may touch paths outside a
temporary directory.

Tests that need extent sharing skip themselves on filesystems without
`FIDEDUPERANGE` support (for example tmpfs).

## Pull requests

Complete the pull-request template: the observable outcome, the effect on
`apply` safety, the container commands you ran and their results, and the
documentation you updated. A green CI run is necessary but does not replace
review of any code that modifies files.
