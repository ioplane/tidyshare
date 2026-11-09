# Tidyshare agent bundle

This portable bundle accompanies the CLI and local MCP server of the same version. It contains operational and developer workflows. It contains no binary or installer.

Verify the archive with the release checksum and attestation. Extract it into an empty private directory. Compare `VERSION` and the `version` and full `commit` in `manifest.json` with the output of the selected binary's `version` command. Both version fields include the leading `v`. Stop on a mismatch or missing commit. For a local development build, use a bundle and binary from the same build with the same full commit. An unqualified `dev` version does not establish a match.

Copy the **whole** extracted `tidyshare-agent-bundle` directory into a client-specific skill directory. Keep `SKILL.md`, `workflows/` and `examples/` together so relative links work. For user-scoped skills, Codex uses `~/.codex/skills/` and Claude Code uses `~/.claude/skills/`. Follow the client's discovery instructions. This bundle does not change client configuration or install hooks.

The JSON examples show local stdio MCP parameters. Claude Code accepts the shown `mcpServers` structure. Codex uses TOML, so translate the parameters into a `[mcp_servers.tidyshare]` entry in its configuration:
```toml
[mcp_servers.tidyshare]
command = "/absolute/path/to/tidyshare"
args = ["mcp", "-root", "/absolute/data/root", "-out", "/absolute/private/report-parent"]
```

Replace each absolute-path placeholder with an appropriate local path. Keep the data root and report parent separate. The examples do not enable cleanup. Add startup permission only after explicit authorization for the intended operation. Read [SKILL.md](SKILL.md) before use.
