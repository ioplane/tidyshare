# Clean orphaned images

Prepare a running owner-bound local Podman API separately. `tidyshare` does
not start services or discover custom stores; use the socket serving the
store being reviewed. See [Match container images](match-container-images.md)
for owner and socket requirements.

Create an exact image plan:

```bash
tidyshare images-plan -owner alice \
  -socket unix:///run/user/1000/podman/podman.sock \
  -out ./image-review -formats json,md
```

`image-review/image-plan.json` records the owner, socket, graphroot, runroot,
full image IDs and candidate evidence. Candidates must be dangling, have no
names or tags, have an explicit zero container count and have no child
images. The reference check includes stopped and external containers such
as Buildah containers. Missing reference or parent evidence prevents cleanup.
Named unused images and parent images are retained.

Review `image-plan.json` and the generated report. Keep only the intended
IDs in `exact_ids` and their corresponding `candidates` entries, preserving
the same order. Save the reviewed file under a new name. Do not change the
owner, socket or store paths to reuse a plan against another store.

Run a dry run, then a one-target canary with a separate report directory:

```bash
tidyshare cleanup -kind images -plan ./image-review/image-plan.json \
  -out ./image-dry-run -formats json,md
tidyshare cleanup -kind images -plan ./image-review/image-plan.json \
  -out ./image-canary -formats json,md -limit 1 -yes
```

Without `-yes`, no image is removed. Execution checks the store identity and
fresh image, child and container references before each removal. An image
that disappears or gains a reference is skipped. Unexpected API errors stop
the operation and preserve the partial report.

The official Podman SDK removes only the reviewed full ID with force and
ignore disabled and parent pruning disabled. Container and volume deletion
and global pruning are unavailable. Keep the store quiet during execution:
reference checks and removal are separate requests, and a concurrent change
can make the engine refuse removal.

Review the canary's `cleanup.json`, `cleanup.jsonl` or `cleanup.md`, depending
on `-formats`, before executing the remaining reviewed IDs. Repeating a plan
skips images that are already absent. Image size totals are logical bytes,
including shared layers; they are not measured recovered filesystem space.
