# Governance

tidyshare uses a maintainer-led model. Maintainers are listed in
[MAINTAINERS.md](MAINTAINERS.md) and own releases, security response,
repository settings and final merge decisions.

## Decision making

Routine changes are decided in pull-request review. Material decisions about
`apply` safety, report formats or the release process are recorded in the
pull request that makes them and summarized in `docs/en/explanation/`.

When consensus is not reached, a maintainer documents the alternatives, the
safety impact and the compatibility impact, then decides. Not losing data takes
priority over convenience and backward compatibility.

## Maintainer responsibilities

Maintainers review code that modifies files, protect release credentials and
branch rules, coordinate vulnerability response, keep releases consistent with
SemVer and Conventional Commits, and disclose conflicts of interest.

## Becoming or leaving as a maintainer

Existing maintainers may nominate contributors with sustained, technically
sound work. A nomination needs unanimous approval from active maintainers.
Maintainers inactive for six months may move to emeritus status; a maintainer
may resign at any time.

## Changes to governance

Governance changes need a public pull request, approval from every active
maintainer and a seven-day review period.
