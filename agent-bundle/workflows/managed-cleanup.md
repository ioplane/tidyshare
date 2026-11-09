# Managed cleanup

Managed cleanup uses a separate `LIMITED` contract. It does not change strict Darwin guards. Stop applications and consumers as required. Observations cannot establish global inactivity.

1. Select an authorized root and an empty private report destination outside target scopes.
2. Run `tidyshare managed-plan -root /absolute/root -kind homebrew -out /absolute/new-plan`. Repeat `-kind` to select categories. Omitting it selects all categories.
3. Review `managed-plan.json`, `managed-plan.md`, target state and scope, and the exact SHA-256 of the plan bytes. `managed-plan` prints this digest on stderr and in the top section of `managed-plan.md`. Do not treat a new digest of changed plan bytes as automatic approval. Review manager and interpreter identity where recorded. Native planning qualifies commands on private scratch fixtures and has side effects there.
4. Run `tidyshare managed-clean -plan /absolute/new-plan/managed-plan.json -out /absolute/new-dry-run`. Review the results and blocked targets.
5. Obtain explicit permission for the exact digest and target paths before execution. Check that the plan and identities remain current.
6. Where a bounded canary is appropriate, execute the approved target with `-plan-sha256 <reviewed-sha256> -target /absolute/target -limit 1 -yes` and a new `-out` directory. `-limit` counts manifest entries or native operations. A skipped entry can consume the limit. A native operation uses the manager scope, not a file whitelist. If this does not provide the required canary, stop for a separate decision.
7. Read `managed-cleanup.json`, `managed-cleanup.md`, `managed-results.jsonl`, and `reviewed-plan.json`. Inspect statuses, partial actions, journal and recovery paths before further action.

MCP execution requires server startup with `-allow-cleanup` and a `cleanup_managed` call with `execute: true`, the reviewed `plan_sha256`, and exact `targets`. Neither gate grants permission by itself. Docker build cleanup remains blocked. Do not invent a prune operation. See [recovery](recovery-diagnostics.md) for failures.
