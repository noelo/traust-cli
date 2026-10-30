#!/bin/bash
# pi-setup: generate the pi agent-dir models.json from environment variables.
# $MODEL_BASE_URL is substituted into the baked template (pi does not
# interpolate env vars in baseUrl; only apiKey/header values). $LLMAPIKEY is
# left as "$LLMAPIKEY" in the generated file — pi interpolates it itself.
# Usage: pi-setup && pi --model deepsec/<model> --print "task"
set -euo pipefail
: "${MODEL_BASE_URL:?MODEL_BASE_URL must be set (from the llm-access Secret)}"
sed "s|\$MODEL_BASE_URL|${MODEL_BASE_URL}|g" /opt/pi-agent/models.json.tmpl \
  > /opt/pi-agent/models.json
echo "pi models.json generated from \$MODEL_BASE_URL (endpoint: ${MODEL_BASE_URL})"
