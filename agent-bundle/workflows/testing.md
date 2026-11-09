# Testing

Record the full source commit, platform, toolchain and exact commands. Use an owned worktree and disposable fixtures. Do not test cleanup against real caches.

The project CI runs these commands in its prepared Podman environment:

```sh
sudo podman compose run --rm modcheck
sudo podman compose run --rm fmtcheck
sudo podman compose run --rm vet
sudo podman compose run --rm lint
sudo podman compose run --rm test
sudo podman compose run --rm race
sudo podman compose run --rm vuln
sudo podman compose run --rm build
```

CI also runs native Darwin with `CGO_ENABLED=1`, the project build tags, `go vet ./...` and `go test -race -count=1 -shuffle=on -timeout=3m ./...`. Record native Darwin results separately from Linux `CGO_ENABLED=0`. For Oracle Linux, record SELinux Enforcing mode, labels and fixture permissions. A private container fixture may need its own label on a task-owned disposable copy. Use a private `:Z` bind label only when needed for that fixture. Do not relabel a shared or current checkout, user data, or other paths. Do not disable enforcement or use sudo to bypass permissions. If private labeling is not allowed, retain the blocked result as a gap.

Exercise CLI and stdio MCP with owned fixtures, including refusal and a failing canary. Record commands, results, skips and retained evidence. A skip or an unavailable platform is a qualification gap, not a pass. Do not install missing tools without a separate decision.
