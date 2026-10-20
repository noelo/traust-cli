#!/usr/bin/env bash
# Fake Codex CLI for traust-cli integration tests: current JSONL schema.
set -eu
cat > /dev/null

printf '%s\n' '{"type":"thread.started","thread_id":"t-1"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"reviewing"}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","command":"ls","aggregated_output":""}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":30,"output_tokens":20}}'
printf '{"findings": []}' > fixture-vuln-findings.json
