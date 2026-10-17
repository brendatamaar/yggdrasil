---
paths: ["internal/core/**"]
---

# Functional core

The rules for `internal/core/**` are in docs/AGENTS.md section 5.11; that is the single source of truth. What follows is specific to Claude Code:

- A hook checks every edit to `internal/core/**` and blocks one that adds an effect (IO, clock reads, randomness, goroutines, logging). Move the effect to the owning package and return a command instead of working around the hook.
- A new `fe:allow` exception needs the human's approval. Ask first; never add one on your own.
- At the end of a turn that changed core files, the hook re-checks them and runs the verify command (`go test ./internal/core/...`). If it fails, fix it before finishing.
