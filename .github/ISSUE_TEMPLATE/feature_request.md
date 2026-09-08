---
name: Feature Request
about: Suggest a new feature for slinit
title: "[FEATURE] "
labels: enhancement
assignees: ''
---

## Description

A clear description of the feature you'd like.

## Use Case

Why is this feature needed? What problem does it solve?

## Proposed Solution

How do you think this should work?

## Alternatives Considered

Any alternative solutions or workarounds you've considered.

## Upstream Parity

If this feature exists in one of slinit's reference sources, please note
which one and the exact name / semantics — matching upstream reduces
implementation risk and preserves muscle memory for admins moving in:

- **dinit** (`../dinit/src/`) — the state-machine + config-format base
- **systemd** — service-manager directive/CLI surface
- **runit** / **s6-linux-init** / **OpenRC** / **upstart** — feature-
  specific ports

If the feature has no upstream analogue, say so — slinit is willing to
ship native features, but the bar for surface-growth without upstream
precedent is intentionally higher (see CONTRIBUTING.md).

## Additional Context

Any other information, code pointers, benchmark data, or examples.
