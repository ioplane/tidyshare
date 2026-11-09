# Use the local MCP server

Run the same tidyshare binary as a local stdio server. Supply an allowed data
root and a separate report directory. Neither directory may contain the other.
The server starts no HTTP listener.

```json
{
  "mcpServers": {
    "tidyshare": {
      "command": "/usr/local/bin/tidyshare",
      "args": ["mcp", "-root", "/srv/share", "-out", "/srv/reports/tidyshare"]
    }
  }
}
```

Adjust the paths to the local machine. Standard output carries MCP messages
only. Each call writes reports in a new private `mcp-*` subdirectory and returns
its path, report paths, an operation summary and the execution flag. Reports
can contain file paths; keep the report parent accessible only to its operator.

| Tool | Input | Effect |
| --- | --- | --- |
| `discover` | Optional `path`, `formats` | Finds language caches and Git trees inside the configured root; writes inventory and `cache-plan.json` |
| `scan` | Optional `path`, `min_size`, `formats` | Reads a directory within the configured root; writes reports and `dups.tsv`; image inventory is disabled |
| `inventory_images` | Optional `formats` | Inventories the fixed image store; writes reports |
| `inventory_workstation` | Optional `project_roots`, `previous`, `min_free_bytes`, `include_runtime` | Inventories scoped workstation data; writes private reports |
| `plan_managed_cleanup` | Optional `path`, `kinds`, `include_runtime` | Creates a managed plan and qualifies native commands on private fixtures |
| `cleanup_managed` | `plan_path`, optional `plan_sha256`, `targets`, `execute`, `limit` | Validates a managed plan; dry run by default |
| `plan_images` | Optional `formats` | Reads the fixed Podman store; writes `image-plan.json` and reports |
| `cleanup_duplicates` | `plan_path`, optional `execute`, `limit`, `formats` | Validates every keeper and duplicate under the configured data root; dry run by default |
| `cleanup_images` | `plan_path`, optional `execute`, `limit`, `formats` | Requires the plan owner and endpoint to match the startup configuration; dry run by default |
| `cleanup_caches` | `plan_path`, optional `execute`, `limit`, `formats` | Validates cache paths and the plan root against the configured root; dry run by default |

`formats` is a comma-separated selection of `json,jsonl,md`; omitted means all
three. `scan.min_size` defaults to 1 MiB. `limit` defaults to zero (all eligible
actions). Scan retains its normal one-hour settling period.

Planning tools have non-destructive annotations because they create reports.
Cleanup tools have destructive annotations, including in the default dry-run
configuration. Annotations are hints; the server enforces the execution gate.

To permit execution after reviewing the cleanup scope, start a separate server
with `-allow-cleanup`. A cleanup call must also contain `"execute": true`.
Without both settings, data cleanup cannot execute. Managed execution also
requires the exact reviewed plan SHA-256 and selected target paths. A call omitting
`execute` remains a dry run even when startup allows cleanup.

```bash
tidyshare mcp -root /srv/share -out /srv/reports/tidyshare \
  -allow-cleanup -image-owner root -image-socket unix:///run/podman/podman.sock
```

The image owner and local Unix socket are fixed at startup. Tools cannot select
another owner or endpoint. Omitting the socket selects the owner's usual local
Podman API socket; the server never starts a Podman service. No socket mount is
needed for file-only operations.

Legacy cleanup plans must be absolute paths under the configured root or report
parent, be regular files, contain no symlink path components and fit within
8 MiB. The server saves a private snapshot before invoking the CLI operation.
Managed plans have a 64 MiB bound and may use paths relative to the report
parent. The managed operation records the reviewed plan bytes in its report. Duplicate source paths and keeper paths must be under the data
root and outside the report parent. Cache targets and the cache plan's root
must also remain within the data root. Plans and reports cannot be placed
inside cache cleanup targets. Symlinks and replaced startup directories
are refused. Keep source trees and their parent directories quiet during calls;
path validation is not a filesystem sandbox against concurrent directory moves.

Subprocess-backed operations have a 30-minute deadline. Core workstation and
managed operations use the request context instead. Cancellation does not roll
back completed cleanup actions. Review private reports before retrying a
cancelled cleanup. Child output
is captured with limits of 8 MiB for stdout and 64 KiB for stderr; raw child
output and Podman responses are never copied into tool errors. MCP input frames
are limited to 1 MiB. This interface exposes no arbitrary shell, file read,
volume deletion or global prune tool.

Managed planning can run native qualification on private scratch fixtures. It
has side effects in that scratch. Strict Darwin guards may return
`UNKNOWN/BLOCKED`. Managed cleanup has a separate `LIMITED` contract. Docker
and OrbStack inventory is read-only. Inventory does not establish consumer
inactivity or APFS reclaim.

A failed scan can retain its staging directory below the call's private report
directory. The tool error identifies this diagnostic location without exposing
raw child stderr. Inspect retained files as partial evidence. Other managed
uncertainty errors can identify additional retained scratch roots. Preserve them
and do not retry cleanup automatically.
