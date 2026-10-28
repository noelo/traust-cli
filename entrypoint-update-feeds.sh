#!/usr/bin/env bash
# Entrypoint wrapper for traust-cli-toolkit-plus-feeds.
#
# Refreshes baked vulnerability databases via `traust-cli feeds update`
# before exec-ing the requested command.  The feeds update command fails
# open: any error is logged and the baked databases are used as-is.
#
# Set SKIP_FEED_UPDATE=1 to bypass (air-gapped deployments, CI caching).
set -u

if [ "${SKIP_FEED_UPDATE:-0}" != "1" ]; then
    timeout "${FEED_UPDATE_TIMEOUT:-120}" \
        /usr/local/bin/traust-cli feeds update \
        --max-age "${FEED_MAX_AGE:-120h}" >&2 || true
fi

exec /usr/local/bin/traust-cli "$@"
