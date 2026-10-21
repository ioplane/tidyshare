# Match container images

`scan` queries the local Podman API through the official Go SDK and matches
image archives found in the tree. It does not execute a container engine.

```bash
tidyshare scan -root /srv/share -out ./report -image-users alice,bob
```

`root` storage is always requested at `unix:///run/podman/podman.sock`.
`-image-users` adds rootless services at
`unix:///run/user/<uid>/podman/podman.sock`. Run as root to query other users;
an unprivileged process can query its own service but reports the root store
as unavailable.

Override endpoints when an owner's API uses a different runtime directory:

```bash
tidyshare scan -root /srv/share -out ./report -image-users alice \
  -image-sockets alice=unix:///run/private-alice/podman.sock
```

Only local Unix endpoints are accepted. The socket must belong to the
requested owner, must not be a symlink and must not be world-writable.
The API's rootless identity must match the owner. Reports record the actual
endpoint, graphroot and runroot. Merely naming an owner does not discover
every custom store belonging to that user; specify the service for the
store being reviewed.

## What is matched

A file is an image archive when it is a tar or gzip stream whose first entry
looks like `docker save`, `podman save` or OCI layout output. The image ID is
the config digest read from `manifest.json` or `index.json`. Layers are not
unpacked.

| Status | Meaning |
| --- | --- |
| `in-store` | every image of the archive is in local storage |
| `partial` | some images are in local storage |
| `absent` | none were found and all requested stores were queried successfully |
| `unknown` | none were found and at least one requested store is unavailable |

`images.md` also shows API disk-usage counts, dangling images and images
without containers. Container commands are excluded from the summary.
Disk-usage statistics are optional: a failed request marks only that summary
as unavailable and preserves a successful image inventory and archive matches.

## Interpreting `in-store`

Local storage is not a backup. An archive marked `in-store` is a candidate for
review (tier 2), not for automatic removal: keep it until the images exist
somewhere else.

## Incomplete stores

A requested store that cannot be queried appears as `store` / `unavailable`
in `images.tsv` and with its diagnostic in `images.md`. A rootless query
requires a running API, but an explicit socket does not require the default
`/run/user/<uid>` directory. The tool does not create runtime directories,
start services or sessions, migrate databases, or recover stores.
Each store has a two-minute request budget; decoded API responses are
limited to 32 MiB. Diagnostics name the failed operation without including
the server's response body.

When no archive image is found and any requested store is unavailable, its
status is `unknown`, not `absent`. A positive `in-store` match remains valid;
`partial` means only some archive IDs were found and is never a cleanup
recommendation. Prepare the owner's API separately if a complete inventory
is needed. For example, in an existing correctly configured owner session:

```bash
podman system service --time 60 unix://"$XDG_RUNTIME_DIR/tidyshare-api.sock"
```

Use a new private socket path: Podman replaces an existing socket at the
supplied endpoint. Service startup can initialize or refresh runtime state;
it is a separate operation from scanning. Preserve the store's configured
graphroot, runroot and namespace. Do not invent a different runroot for a
dormant store or run `system migrate` or `system reset` to obtain inventory.

Image names come from native Podman `Names`, with `RepoTags` as a fallback.
Placeholder names are ignored. `local-untagged` does not prove that an image
is unused or removable: intermediate and shared images may have no name.
The tagged-without-containers total includes only named images with an
explicit zero container count. Image sizes include shared layers and must
not be summed as recoverable disk space.
