# Workstation inventory design

Implement the approved inventory, managed cleanup, then Darwin research stages in that order.
This document specifies stage 1. Later stages keep their own plans and acceptance evidence.

## Goal

Expose one bounded workstation inventory through CLI and MCP. Cover Go, npm, uv, Homebrew downloads, Chrome disk cache, Xcode DerivedData and optional runtime storage. Preserve Linux discovery and strict Darwin cleanup behavior.

## Behavior

The workstation command requires -root and -out. It accepts repeated -project-root, optional -previous, -min-free-bytes, -engine and -docker-context.
The root anchors known user cache locations. Only explicit project roots receive recursive Git discovery. Do not recursively scan the home directory.
Select known paths by platform. Preserve recognized custom cache roots from current environment settings when they remain inside the anchor.
Missing optional paths are absent. Existing unreadable paths and cancelled scans are partial, with reasons and a nonzero exit status.
Reports use a separate schema version 1. Write workstation.json and workstation.md with private permissions.
Record host, platform, architecture, time, roots, protected policy, category coverage, logical bytes, allocated bytes and file counts.
Count hardlinks once within each measured category. Do not sum overlapping categories or Docker and filesystem measurements.
Rank complete measurements by allocated bytes. Unknown measurements follow them. No safety score or reclaimed-space prediction is allowed.
Show filesystem available bytes per device. A user budget has no default threshold.
Compare an explicit previous report only when host, platform, roots and policy match. Compare each device and runtime only when its identity also matches.
MCP uses the same implementation and startup root, policy, report directory and daemon identity. Inputs and previous reports cannot expand scope.
Protect browser profiles, Photos and application user data without excluding separate cache roots. Temporary application bundles remain outside cleanup.

## Boundaries

Do not delete user data, stop sessions, change runtime context, install tools or request privileges.
Keep strict Darwin UNKNOWN/BLOCKED. Keep uv inventory-only.
Do not publish, merge or install changes. Store private historical logs only in execution evidence outside Git.

## Decisions

TRIZ: ordinary engineering for inventory and adapters. Darwin remains measure_first because mapping identity is unresolved.
Use existing Go dependencies and report helpers. Do not add a daemon or database. History is an explicit previous report.
