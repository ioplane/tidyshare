---
name: tidyshare
description: Use when inspecting scoped workstation storage or reviewing Tidyshare plans through its CLI or local MCP server.
---

# Tidyshare

Use this skill only with the binary from the same build. Compare `VERSION` and the full source commit in `manifest.json` with `tidyshare version`. Stop if either differs or the commit is missing. Verify the release checksum and attestation before trusting a downloaded archive. The manifest verifies payload completeness, not archive provenance.

Select the smallest authorized root and a separate private report directory. Review capabilities and uncertainty before any cleanup. This skill does not authorize cleanup. Obtain explicit permission for selected targets before execution.

- [Inventory and review](workflows/inventory-review.md)
- [Managed cleanup](workflows/managed-cleanup.md)
- [Recovery and blocked diagnostics](workflows/recovery-diagnostics.md)
- [Adapter development](workflows/adapter-development.md)
- [Testing](workflows/testing.md)
- [Release](workflows/release.md)
- [CLI examples](examples/cli.md)
- [Codex MCP example](examples/codex-mcp.json)
- [Claude Code MCP example](examples/claude-code-mcp.json)

Strict Darwin cache guards can return `UNKNOWN/BLOCKED`. Managed cleanup has a separate `LIMITED` contract. Docker and OrbStack observation is read-only. Never infer inactivity from unknown observations or reclaim from inventory bytes.
