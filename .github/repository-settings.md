# Repository settings

This file documents settings that cannot be enforced from the source tree.

## General

- Visibility: public
- Default branch: `main`
- Issues, Discussions and private vulnerability reporting: enabled
- Projects and Wiki: disabled
- Merge method: squash only
- Automatically delete head branches: enabled

## Branch protection for `main`

- Require pull requests; do not require approving reviews while the project has
  a single maintainer.
- Require conversation resolution and linear history.
- Require branches to be up to date.
- Require the `required` and `Dependency review` checks from GitHub Actions.
- Block force pushes and deletions.
- Restrict creation of `v*` tags to repository administrators.

## Actions and security

- Allow GitHub-authored, verified-creator and explicitly approved actions only.
- Require actions to be pinned to full commit SHAs.
- Default workflow token permission: read repository contents.
- Enable Dependabot alerts and security updates, secret scanning, push
  protection, dependency graph and code scanning.
- CI runs on Blacksmith runners (`blacksmith-2vcpu-ubuntu-2404`,
  `blacksmith-4vcpu-ubuntu-2404`); the Blacksmith GitHub App must be installed
  for the organization and have access to this repository.

Settings are reviewed after workflow names or required checks change and before
every first release on a new major line.
