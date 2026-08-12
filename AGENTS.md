Exit code: 0
Wall time: 0.5 seconds
Output:
# VPS Node Auditor project instructions

This file contains repository-wide guidance for coding agents. Repository files
and live verification are authoritative. `requirements/PROJECT_REQUIREMENTS.md`
is the product and compatibility specification and supersedes pre-v1 notes.

## Repository identity

- Formal project name: **VPS Node Auditor**.
- Stable daily command: `vna`.
- License: Apache License 2.0.
- This public repository is the source of truth for ongoing development.
- Do not import code, history, configuration, or deployment evidence from other
  checkouts unless the maintainer explicitly requests that operation.

## Product boundary

- Keep administration owner-only over SSH.
- Do not add a web UI, user registration/login, subscription endpoint, public
  management API, remote SSH controller, arbitrary shell execution, or public
  listener.
- Preserve one beginner-facing entry point: `sudo vna`. In a TTY it opens
  the Chinese menu; without a TTY it prints help instead of waiting for input.
- Preserve the scriptable commands documented in
  `requirements/PROJECT_REQUIREMENTS.md`.
- Each device has an independent credential. Normal CLI, logs, diagnostics,
  tests, issues, and CI output must never reveal complete UUIDs.
- Preview or dry-run behavior, `--apply`, and `reload_verified` are deleted
  pre-v1 interfaces. Do not restore them in v1.x.
- Do not add application allowlists, `MATCH,DIRECT` policy generation, or
  server-side application rejection rules unless the product scope is
  explicitly changed.

## Security and private data

- Never commit or publish real server IPs or hostnames, SSH material, UUIDs,
  Stats identities, Reality SNI or short IDs, client YAML, SQLite/WAL/SHM
  files, logs, backups, production configs, personal paths, or deployment data.
- Do not read, copy, cache, print, or upload an SSH private key. Let the user
  enter SSH key passphrases interactively.
- Treat generated client YAML and complete credentials as secrets, including
  temporary files and test output.
- Keep Stats API loopback-only and avoid adding externally reachable control
  surfaces.
- Before a public push or release, scan the current tree and relevant Git
  history with both Gitleaks and TruffleHog. A scanner error or ambiguous
  nonzero exit fails the gate.
- Keep dependency licenses documented. Stop release work on unknown or
  incompatible licenses.

## Compatibility and transactional invariants

- v1.x retains public CLI semantics and compatible configuration and database
  evolution. Reserve breaking CLI, config, and schema changes for v2.0.0.
- SQLite migrations are forward-only, transactional, and history-preserving.
- Accepted credential writes take the global operations lock, create a unique
  private rollback point, validate a candidate, atomically replace the config,
  perform a controlled sing-box restart, verify the service, new MainPID, port
  443, Stats API, executable/config identity and readability, and only then
  commit SQLite.
- On failure, restore the prior config, service state, client YAML state, and
  database state as applicable. Never report success after a permission or
  rollback failure.
- `remove-user` revokes all active devices atomically while retaining user,
  credential, and traffic history. Remove active YAML only after commit.
- The database directory must be owned by its service account and use mode
  `0700`; SQLite, WAL, and SHM files must match its ownership and use mode
  `0600`. Preserve the full production ownership rules in the requirements.
- Counter identity is system boot ID plus systemd MainPID plus that PID's Linux
  start time. Treat uncertain counter regression as a gap and never rebill it.
- `vna check` is read-only, does not repair state, and never outputs UUIDs.

## Development workflow

- Keep `main` stable. Create a `codex/<topic>` branch before implementation.
- Inspect `git status` and staged and unstaged diffs before every commit.
  Preserve unrelated changes.
- Stage only explicitly reviewed paths. Do not use `git add .`, `git add -A`,
  or `git add --all`.
- Use focused commits in `<type>: <description>` form. Do not amend, rebase,
  force-push, move a published tag, or rewrite public history without explicit
  user authorization.
- Pushes, pull requests, releases, deployments, destructive operations, and
  production changes each require separate authorization.
- Prefer GitHub CLI (`gh`) for repository, Actions, PR, and Release operations
  when it is installed and authenticated. Never expose its credential store or
  tokens.
- Use only process- or command-scoped proxy settings when GitHub access needs a
  local proxy; do not persist a global Git or system proxy for one task.

## Verification

Run checks proportionate to the change. Before a release or production
deployment, run the complete gate:

1. `gofmt` check and `git diff --check`.
2. `go vet ./...`.
3. `go test -count=1 ./...`.
4. Build all five Linux AMD64 static binaries with the intended version:
   `vna`, `node-audit`, `node-audit-collector`, `node-audit-maintenance`, and
   `node-provision`; verify `vna version` exactly.
5. ShellCheck, Python compilation and tests, and Linux rollback fault injection.
6. `govulncheck ./...`.
7. Gitleaks and TruffleHog.
8. Dependency-license review and SHA-256 manifests for release assets.

Do not weaken, skip, or mark a failing check as allowed merely to obtain a
green result. When behavior is environment-specific, improve diagnostics while
preserving equivalent coverage.

## Production workflow

- Begin with a read-only preflight. State the next command and its impact before
  each stage, and stop on the first failure.
- Create and verify an independent rollback point before making changes.
- Upload artifacts to an isolated random directory and verify SHA-256 on the
  server.
- Preserve all real users, credentials, YAML, and audit history during upgrades.
- Verify services, the new sing-box MainPID, port 443, Stats API, collector
  progress, timers, permissions, and `vna check` after installation.
- For release acceptance, create a temporary `release-smoke` credential, let
  the user perform the real client test, revoke it atomically, then prove its
  YAML is gone while history and existing users remain intact.
- If a stage fails, prove rollback restored the previous service, runtime
  credentials, database, and permissions before reporting recovery.

## Documentation discipline

- Update public documentation when behavior, commands, compatibility, security
  boundaries, deployment, or release procedure changes.
- Keep this file limited to durable, public repository guidance. Track dated
  CI failures, release status, and maintenance work in issues or pull requests.
- Do not add conversation transcripts, machine-specific paths, private history,
  or deployment details to this repository.

## Code review rules

- Flag any complete credential or deployment identity that can reach normal
  output, logs, diagnostics, tests, issues, or CI.
- Flag write paths that bypass the global lock, rollback point, candidate
  validation, atomic replacement, production verification, or SQLite commit
  ordering.
- Flag counter-regression handling that can rebill uncertain traffic.
- Flag changes that broaden the owner-only SSH boundary or revive a deleted
  pre-v1 interface.
- Flag v1.x CLI, config, or schema changes that are not backward compatible.

