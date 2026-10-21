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
- Line wrapping adapts to the terminal width everywhere. Monitor transcript
  lines wrap (ANSI-aware) instead of truncating, and re-flow on resize;
  the header and status bar clip to fit one line at any width; the report
  browser rebuilds its table columns and re-renders its markdown at the
  current width; plain (non-TTY) output and summary lines wrap to the
  terminal width instead of a fixed 200 characters.
- crush runs now execute through `crush server` in client mode: thinking,
  tool calls and cumulative token usage stream live into the monitor
  (run_complete is correlated by RunID), budget bands and spend
  declarations work on real usage, and aborts cancel the remote session
  through the cancel endpoint before teardown. Permission and question
  requests cannot be satisfied unattended and fail closed (exit 2) naming
  the tool. Runs fall back to `crush run` when the server cannot start.
- Full-permission consent: `traust-cli init` asks whether to run skills
  with all tools auto-approved (the harness's skills require unattended
  bash and file writes) and records the answer in the workspace state;
  headless installs pass `--full-permission`. Runs translate the consent
  into the right flag per harness — claude
  `--dangerously-skip-permissions`, crush `--yolo` / the server's
  permissions-skip endpoint, codex
  `--dangerously-bypass-approvals-and-sandbox`, pi's full toolset — and
  surface it in the notes, the run journal and `doctor`. Without consent,
  permission requests still fail closed (exit 2) with guidance.
- Every remaining print path now respects the terminal width: the
  full-permission and standalone-profile warnings, the run summary's
  harness-error block (source indentation from harness error boxes is
  stripped so re-wrapped lines start at the left edge) and artifact lines,
  doctor details, init step logs, and the skills table (which drops its
  description column below ~77 columns instead of overflowing).
- Interactive screens now gate on a real terminal check (ioctl) instead of
  "is a character device": `traust-cli skills < /dev/null` used to open the
  picker and hang forever, because /dev/null is a character device.
- Cobra help output respects the terminal width: flag usages wrap with
  continuations aligned under each description (FlagUsagesWrapped at the
  measured width), Long descriptions re-wrap, the trailing use-hint breaks
  on narrow terminals, cobra's built-in completion commands are hidden from
  the command list (still invocable), over-long command summaries were
  shortened, and the init --forge default URL moved out of the flag line
  (an unbreakable token that could not fit pflag's description column).
- Runs execute in the background by default: `traust-cli run` spawns a
  detached worker process (setsid, survives the terminal) and attaches to
  it; `--detach` fires and forgets. The run journal is the event bus:
  `attach` replays it and follows it live (Ctrl-C detaches without
  cancelling — x twice cancels), `runs` lists run states, `stop` cancels
  through a graceful SIGINT/SIGTERM/SIGKILL ladder, `logs` renders the
  transcript and `result` prints the stored outcome with the run's own
  exit code. Attaching to a finished run replays the full transcript.
- The monitor now shows tool results (crush tool_result parts, truncated),
  which are the bulk of a run's output; journal raw lines no longer
  duplicate parsed events, so attach replays render once.
- Fixed the detach hang: the attach monitor's relay goroutine waited for
  the journal tailer to finish, but a detached run keeps tailing — so the
  process (and the terminal) stayed open until a second ctrl+c killed it.
  The relay now abandons the feed the moment the monitor exits; one ctrl+c
  detaches cleanly, printing the re-attach hint and exiting 0.
- Multi-stage flows: `flow audit-triage` (audit then triage, outputting
  triaged findings), `flow remediate-verify` (patch then verify against a
  patched checkout with the generated patches applied) and `flow full`
  (the whole loop). Stages chain through their artifacts — the audit
  report feeds triage (--auto for headless), triaged findings feed patch,
  verify audits a fresh clone with the patches applied — and every stage
  is an ordinary background run. `flow status` lists flows and stage
  detail; interactive flows follow stage-by-stage with Ctrl-C detaching
  without cancelling.
- `peek <run-or-flow>`: a one-shot snapshot of what is happening right now
  — state, usage and the last dozen journal entries, printed and gone. A
  flow peek shows the stage chain and recurses into the current stage's
  run.
- The init wizard's full-permission confirm now defaults to Yes (the
  recommended choice) and its warning spells out that runs and flows
  (audit → triage → patch → verify) fail closed without it; doctor's
  scoped-mode message says the same.
