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
