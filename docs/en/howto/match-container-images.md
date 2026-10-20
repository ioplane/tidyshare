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
| `absent` | none are in local storage |

`images.md` also shows `podman system df`, dangling images and images without
containers.

## Interpreting `in-store`

Local storage is not a backup. An archive marked `in-store` is a candidate for
review (tier 2), not for automatic removal: keep it until the images exist
somewhere else.
