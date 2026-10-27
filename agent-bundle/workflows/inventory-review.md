# Inventory and review

1. Verify the bundle and binary version and commit as described in [README](../README.md).
2. Select an authorized data root and a separate private report directory.
3. Run `tidyshare doctor -root /absolute/root -json`. Review the platform and capability results.
4. Run `tidyshare workstation -root /absolute/root -out /absolute/new-report`. Add `-project-root` for each explicitly selected project. The optional `-min-free-bytes` is a filesystem budget.
5. Read `workstation.json` and `workstation.md`. Review unknown observations and proposed operations.

Inventory accounting does not measure APFS reclaim. Missing consumer observations do not establish inactivity. Strict Darwin guards may return `UNKNOWN/BLOCKED`. Docker and OrbStack inventory is read-only. A proposed operation is not permission to execute it.
