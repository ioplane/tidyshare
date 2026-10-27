# Adapter development

Use a separate owned worktree and scratch directory. Record the source commit and platform. Read the existing adapter, registered CLI and MCP callers, schemas, capability guards and refusal paths before changes. Preserve unknown and blocked outcomes when observation or capability is incomplete. Do not add a privileged fallback.

Use synthetic fixtures inside the owned scratch directory. Verify selected and outside-marker data. Bound native execution and preserve process ownership and failure evidence. Verify platform-specific behavior on its native platform. Record unsupported platforms and skipped tests as gaps. Do not treat Docker or OrbStack inventory as permission to prune.
