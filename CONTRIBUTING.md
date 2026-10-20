# Contributing to traust-cli

Thanks for contributing!

## Ground rules

- **Never reimplement engine logic.** Every deterministic operation is a call
  through `internal/api` (the `TraustAPI` interface) to
  `python3 -m traust.cli` in the managed venv. If you find yourself porting a
  gate, a resolver or a validator into Go, stop — extend the interface and
  delegate instead. This seam is also how the future all-Go engine slots in
  operation by operation.
- **Fail closed on permissions.** A harness that cannot express a tool grant
  gets the narrower default plus an honest note, never a silent
  `--dangerously-skip-permissions` equivalent.
- **Every adapter states its caveats** in `Notes()`; the runner prints them
  before every run.
- **S9 always**: the agent cwd is the harness root, never the audited
  checkout. Do not add code that launches a harness inside a target repo.

## Workflow

1. Fork, branch, commit. `make check` (build + vet + test) must pass.
2. Tests that touch the run pipeline use the fake harness binaries in
   `testdata/fake-harnesses/` — no API keys, no network.
3. Golden argv tests (`internal/harness/adapters_test.go`) pin every adapter's
   invocation; update them deliberately, never blindly.
4. If you touch `internal/catalog/data/standalone.yaml`, run
   `traust-cli catalog check --traust-root <a traust checkout>` to verify the
   profile against the real tree.

## Commit style

Conventional-ish, imperative mood, describing the user-facing impact:
`Add budget refuse-band enforcement to the run monitor`, not
`fix runner.go`.
