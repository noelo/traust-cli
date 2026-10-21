#!/usr/bin/env bash
# Fake Claude Code for traust-cli integration tests.
#
# Consumes the prompt on stdin, emits a canned stream-json transcript, and
# writes a fixture artifact into the working directory (the fake harness
# root) matching the skill named in the prompt:
#   triage    → fixture-triage.json
#   patch     → PATCHES.json + PATCHES/bug_01/patch.diff
#   otherwise → fixture-security-audit.json
# FAKE_CLAUDE_MODE selects a scenario:
#   needs-input  emits an AskUserQuestion tool_use
#   fail         writes an error to stderr and exits 3
#   partial      writes a report but exits 1 (failed run, partial output)
#   slow         sleeps 30s first (background/stop testing), then completes
set -eu
PROMPT="$(cat)"

if [ "${FAKE_CLAUDE_MODE:-}" = "needs-input" ]; then
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{}}]}}'
  exit 0
fi

if [ "${FAKE_CLAUDE_MODE:-}" = "fail" ]; then
  echo "ERROR Agent processing failed: 401 Unauthorized invalid x-api-key" >&2
  exit 3
fi

if [ "${FAKE_CLAUDE_MODE:-}" = "slow" ]; then
  printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}'
  sleep 30
fi

printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"starting audit"}]}}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"stage complete","usage":{"input_tokens":100,"output_tokens":50},"total_cost_usd":0.42}'

if printf '%s' "$PROMPT" | grep -q 'harness skill "triage"'; then
  printf '{"findings": [], "triage_completed": "2026-10-21"}' > fixture-triage.json
elif printf '%s' "$PROMPT" | grep -q 'harness skill "patch"'; then
  mkdir -p PATCHES/bug_01
  printf '{"patches": [{"bug": "bug_01", "status": "generated"}]}' > PATCHES.json
  printf 'diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-one\n+two\n' > PATCHES/bug_01/patch.diff
else
  printf '{"findings": [], "metadata": {"harness_version": "test"}}' > fixture-security-audit.json
fi

if [ "${FAKE_CLAUDE_MODE:-}" = "partial" ]; then
  exit 1
fi
