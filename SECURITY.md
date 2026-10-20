# Security Policy

## Reporting a vulnerability

Do not open a public issue for security vulnerabilities. Report them
privately to the maintainers via GitHub security advisories
(`Security` → `Report a vulnerability`) for this repository.

## Design posture

- **S9 (repo-config isolation).** The CLI launches headless agents with their
  working directory at the harness root, never inside an audited checkout.
  Repository-supplied agent configuration (`.claude/`, `.crush/`, `.pi/`,
  `AGENTS.md`, `CLAUDE.md`, hooks, MCP config) from a target under audit is
  data, never configuration.
- **Fail-closed permissions.** Tool grants come from the skill's declared
  `allowed-tools` where the harness can express them. Full-permission flags
  are opt-in (`--unsafe-full-permission`) and recorded in the run journal.
- **No stored secrets.** Harness credentials belong to the harness; the
  ledger bearer token is read from an environment variable named on the
  command line. The CLI stores variable names, never values.
- **Pinned, verified bootstrap.** uv is downloaded from a pinned GitHub
  release and verified against the published checksum. No piped-to-shell
  installers.
