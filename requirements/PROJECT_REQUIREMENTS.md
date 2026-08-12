# VPS Node Auditor v1.0.0 requirements

This file supersedes all pre-v1 design notes.

## Product boundary

- Owner-only administration over SSH; no web UI, registration, public API, or subscription endpoint.
- `vna` is the stable daily command. With a TTY, `sudo vna` opens the Chinese menu; without a TTY it prints help.
- Direct commands remain scriptable: `create`, `users`, `rotate`, `disable`, `remove-user`, `traffic`, `user`, `month`, `status`, `check`, `logs`, `timers`, `backup`, `restore`, and `version`.
- Each device has an independent UUID. UUIDs, Stats names, real endpoints, SNI, short IDs, client YAML, databases, and logs are private deployment data.

## Write operations

- `create` executes immediately.
- `rotate`, `disable`, and `remove-user` require terminal confirmation; non-interactive callers must pass `--yes` to `vna`.
- Every accepted change takes the global operations lock, creates a private unique rollback point, validates the candidate, atomically replaces the sing-box config, restarts sing-box, verifies the new MainPID, port 443, Stats API, service unit/executable/config identity and config readability, then commits SQLite.
- Any failure restores the previous configuration, service, client YAML state, and database state as applicable.
- `remove-user` revokes every active device in one config change, one restart, and one SQLite transaction. User, credential, and traffic history remain; active YAML files are removed only after commit.
- Preview/dry-run behavior and the old apply/reload interfaces do not exist in v1.0.0.

## Consistency and permissions

- `vna status` is a summary. `vna check [--json]` is read-only and checks sing-box, port 443, Stats, collector freshness, runtime/SQLite/Stats mappings, active and revoked YAML, and file permissions.
- Runtime configuration versus SQLite mismatch is an error. Missing active YAML is a warning. Checks never print UUIDs.
- Production modes and owners are: auditor config `root:node-audit 0640`, provision config `root:root 0600`, sing-box config mode `0640` and readable by its actual service identity, database directory `node-audit:node-audit 0700`, and SQLite/WAL/SHM files matching the directory owner with mode `0600`.
- Counter epochs use system boot ID plus systemd MainPID plus that PID's Linux start time. If MainPID is unavailable, counter regression is a gap and is never re-billed.

## Release gates

- Format, vet, tests, Linux AMD64 static builds, shell/Python checks, vulnerability scan, and secret scans must pass.
- Deployment begins with a read-only preflight and an independent rollback point. Uploaded artifacts require SHA-256 verification.
- A temporary `release-smoke` credential must pass a real client connection test and then be revoked without affecting existing users.
- The public candidate is exported from the exact verified private commit to an absent or empty sibling directory, contains no Git history or private deployment data, and is scanned again.
- No public Git initialization, commit, tag, GitHub repository, or push is permitted until a project license is selected. Publishing requires separate explicit authorization.

## Compatibility

- v1.x retains public command semantics and compatible config/database evolution.
- SQLite migrations are forward-only, transactional, and preserve history.
- Breaking CLI/config/schema changes are reserved for v2.0.0.
