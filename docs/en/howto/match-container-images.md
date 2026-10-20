# Match container images

`scan` lists podman images and matches image archives found in the tree.

```bash
tidyshare scan -root /srv/share -out ./report -image-users alice,bob
```

`root` storage is always listed. `-image-users` adds rootless storage of the
named users (the tool runs `podman` as each user through `runuser`, so run
`scan` as root for that).

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

`images.md` also shows `podman system df`, dangling images and images without
containers.

## Interpreting `in-store`

Local storage is not a backup. An archive marked `in-store` is a candidate for
review (tier 2), not for automatic removal: keep it until the images exist
somewhere else.

## Incomplete stores

A requested store that cannot be queried appears as `store` / `unavailable`
in `images.tsv` and with its diagnostic in `images.md`. A rootless query
requires an existing private runtime directory owned by that user. The tool
does not create runtime directories, start user sessions or recover stores.

When no archive image is found and any requested store is unavailable, its
status is `unknown`, not `absent`. A positive `in-store` match remains valid;
`partial` means only some archive IDs were found and is never a cleanup
recommendation. Restore the owner's session separately if a complete
inventory is needed.

Image names come from native Podman `Names`, with `RepoTags` as a fallback.
Placeholder names are ignored. `local-untagged` does not prove that an image
is unused or removable: intermediate and shared images may have no name.
The tagged-without-containers total includes only named images with an
explicit zero container count. Image sizes include shared layers and must
not be summed as recoverable disk space.
