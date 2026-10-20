# traust-cli

[![ci](https://github.com/traust-security/traust-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/traust-security/traust-cli/actions/workflows/ci.yml)

A single Go binary that is the front door to the
[traust](https://github.com/traust-security/traust) security workflow: it sets
up the environment, drives harness skills headlessly through the agent of
your choice, validates the output, and shows you the results.

```bash
traust-cli init                          # uv + traust checkout + config home, one command
traust-cli skills                        # browse the catalog (TUI)
traust-cli run threat-model /src/myapp   # first audit, through YOUR agent harness
traust-cli report myapp-threat-model.md  # validate, lint, browse
```

The CLI is deliberately a thin, honest layer. Every deterministic operation
(`reporting validate`, `registry models resolve`, `toolchain setup`, …) is
delegated to the traust engine CLI from the managed venv — the CLI adds the
user experience, not a second engine.

## Why

Running traust today means cloning the harness repo, `uv sync`, creating
`TRAUST_CONFIG_HOME` from templates, optionally installing a scanner
toolchain, then driving skills by hand from inside an agent. traust-cli wraps
all of that, and adds the pieces the harness documents as gaps:

- **Model-floor pre-flight** — a run's model defaults to the registry's floor
  resolution for the skill's role (`traust registry models resolve`);
  explicit overrides are warned about and journaled
  (traust/docs/model-routing.md documents the missing runtime enforcement).
- **Budget bands** — the shipped `budget-policy.yaml` per-run output-token
  caps and warn/degrade/refuse bands, enforced live (warn 1.5×, degrade 2×,
  refuse 3× by default; `--budget-override "<reason>"` records an exception).
- **S9 isolation, centralized** — agents always run from the harness root,
  never inside the audited checkout, and never load target-repo agent
  configuration. Enforcing this in one launcher is stronger than hoping every
  script does it right.
- **Spend declaration** — every run with reported usage appends a
  `registry models spend` row automatically.
- **Run journals** — spec, raw stream, normalized events and result under
  `~/.traust/cli/runs/<id>/`.

## Harness support

Priority order: **claude code → codex → pi → opencode → crush → goose**.
Antigravity (agy) is deferred: it has no headless execution path.

| Harness | Headless invocation | Live stream | Usage | Permission posture |
|---|---|---|---|---|
| claude | `claude -p --output-format stream-json` | ✓ | ✓ | acceptEdits + the skill's `allowed-tools` (fail-closed); `--unsafe-full-permission` for the rest |
| codex | `codex exec - --json --output-last-message` | ✓ | ✓ | `workspace-write` sandbox, approvals never |
| pi | `pi --mode json` | ✓ | ✓ | tool grants mapped onto pi's builtin tools |
| opencode | `opencode run --format json --auto` | partial | — | `--auto` |
| crush | `crush server` (client mode): workspace + SSE events, clean cancel; falls back to `crush run` | ✓ live | ✓ | workspace permission level; unanswered permission/question requests fail closed (exit 2) |
| goose | experimental (`TRAUST_CLI_GOOSE_ARGS` overrides the template) | — | — | goose's own configuration |

Every adapter prints its honest caveats before a run. `--unsafe-full-permission`
is opt-in and journaled.

## Install

```bash
go install github.com/traust-security/traust-cli@latest
```

or grab a static binary from the [releases](https://github.com/traust-security/traust-cli/releases)
(linux/macOS/windows, amd64/arm64, checksummed). The only other thing you
need is an agent harness you are logged into.

## Commands

| Command | What it does |
|---|---|
| `init` | Bootstrap the managed workspace: pinned uv (checksum-verified, never `curl \| sh`), traust checkout at a stable tag, `uv sync`, `TRAUST_CONFIG_HOME` from templates via `install_traust`, optional toolchain profile. Idempotent; nothing is written to your shell profile (use `eval "$(traust-cli env)"` if you want it). |
| `doctor` | CLI checks + `install_traust --doctor`; `--json` for CI. |
| `update` | Move the pin to a newer tag and re-sync. |
| `skills` | Catalog parsed from the same SKILL.md frontmatter the harness reads; TUI picker on a TTY, table otherwise; `--json`. |
| `run <skill> [args…]` | Compose the SKILL.md into a prompt, pre-flight the model floor, drive the harness headlessly, journal everything, discover and validate artifacts, declare spend. Live TUI monitor on a TTY. |
| `report <file-or-dir>` | Validate (`reporting validate`), lint threat models (`reporting lint`), render (`reporting render`), and browse findings (severity-colored TUI). |
| `ledger whoami\|findings\|layers\|verify` | Read-only views over a ledger service (`--url` or `TRAUST_LEDGER_URL`; the token comes from the env var named by `--token-env`). No submissions, no countersign. |

### Exit codes (a contract)

| Code | Meaning |
|---|---|
| 0 | run completed |
| 1 | error (harness failure, failed artifact validation, aborted) |
| 2 | the skill needs interactive input a headless run cannot provide |
| 3 | the budget refuse band was hit |

## Security notes

- The audit target is always data, never configuration (harness rule S9):
  the agent's cwd is the harness root; target-repo `.claude/`/`AGENTS.md`/
  hooks are never loaded as agent config.
- Credentials: the CLI never stores secrets. Harness logins are the
  harness's own; the ledger token is read from an environment variable you
  name.
- uv is installed from a pinned, checksum-verified GitHub release — no
  piped-to-shell installers.

## Development

```bash
make check        # build + vet + test
make lint         # staticcheck
go test ./...     # integration tests run against fake harness binaries in
                  # testdata/fake-harnesses/ — no API keys needed
```

The `catalog check` command compares the embedded standalone profile
(`internal/catalog/data/standalone.yaml`, seeded from traust's
docs/standalone-usage.md and model-classes.md) against a traust checkout;
CI runs it on every push to catch drift.

## License

Apache-2.0 — see [LICENSE](LICENSE). Part of the
[traust](https://github.com/traust-security) family.
