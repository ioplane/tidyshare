# Pull request

## Outcome

<!-- Describe the observable result, not the implementation sequence. -->

## Safety and compatibility

- [ ] `scan` stays read-only; no new write outside `-out`.
- [ ] Any change to `apply` keeps dry run as the default and re-checks size and
      mtime before touching a file.
- [ ] CLI flags, report columns and exit codes are unchanged, or the change is
      marked as breaking.
- [ ] New behavior started with a failing test and includes refusal cases.
- [ ] Public behavior is documented under `docs/en/`.

## Container verification

<!-- List the exact Compose services you ran and their results. -->

- [ ] `modcheck`
- [ ] `fmtcheck`
- [ ] `vet`
- [ ] `lint`
- [ ] `test`
- [ ] `race`
- [ ] `vuln`

## Release note

<!-- Use a Conventional Commit title and state the user-visible impact. -->
