#!/usr/bin/env bash
# Fake Claude Code for traust-cli integration tests.
#
# Consumes the prompt on stdin, emits a canned stream-json transcript, and
# writes a fixture report into the working directory (the fake harness root).
# FAKE_CLAUDE_MODE=needs-input emits an AskUserQuestion tool_use instead.
set -eu
cat > /dev/null

if [ "${FAKE_CLAUDE_MODE:-}" = "needs-input" ]; then
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{}}]}}'
  exit 0
fi

printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"starting audit"}]}}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"audit complete","usage":{"input_tokens":100,"output_tokens":50},"total_cost_usd":0.42}'
printf '{"findings": [], "metadata": {"harness_version": "test"}}' > fixture-security-audit.json
