# Changelog

## 0.1.0 — unreleased

Initial implementation.

- Managed workspace bootstrap (`init`, `doctor`, `update`, `env`): pinned
  checksum-verified uv, traust checkout at a stable tag, `uv sync`, config
  home via `install_traust`, optional scanner toolchain profiles.
- Skill catalog parsed from SKILL.md frontmatter with a shipped standalone
  profile and drift guard (`skills`, `catalog check`).
- Headless skill execution through claude code, codex, pi, opencode, crush
  and an experimental goose adapter (`run`), with a live TUI run monitor,
  model-floor pre-flight, budget warn/degrade/refuse bands, run journals,
  artifact discovery + schema validation + rendering, and spend declaration.
- Read-only report browsing (`report`) and ledger views (`ledger`).

### Fixes from first field use

- Budget policy parsing accepts the shipped sequence-form `bands:`
  (`- {at: 1.5, action: warn}`) in addition to the map form.
- Harness failures now surface the stderr tail (provider 401s are visible
  without opening the journal) and are recorded in `result.json` as
  `harness_error`.
- Pre-flight model availability check (crush: verified against
  `crush models`): unrunnable models fail fast with actionable guidance
  instead of an authentication error two seconds into the run.
- The run monitor no longer looks frozen on quiet harnesses: the status bar
  shows how long since the last output, and ctrl+c requires a confirming
  second press within six seconds — a single stray ctrl+c cannot kill a
  long-running audit.
- Force-quitting the monitor (or any early exit) now tears the harness down
  and joins the runner before returning: no more "run produced no result",
  and no send-on-closed-channel races.
- Failed, refused and aborted runs still discover and validate their
  artifacts — partial output and checkpoint state are visible exactly when a
  run dies mid-flight.
- The run summary (artifacts, journal path, harness error, final message)
  prints after the interactive monitor exits, not only in plain mode.
