# CLI examples

Replace the absolute placeholders with authorized paths. Use new private report directories. Check the matching version and commit first.

```sh
tidyshare version
tidyshare doctor -root /absolute/root -json
tidyshare workstation -root /absolute/root -out /absolute/new-inventory
tidyshare managed-plan -root /absolute/root -kind homebrew -out /absolute/new-plan
tidyshare managed-clean -plan /absolute/new-plan/managed-plan.json -out /absolute/new-dry-run
```

Planning can run native qualification in private scratch. Review the plan, digest and results before requesting any cleanup. See [managed cleanup](../workflows/managed-cleanup.md).
