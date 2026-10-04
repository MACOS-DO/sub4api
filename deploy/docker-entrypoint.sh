#!/bin/sh
set -e

# Fix data directory permissions when running as root.
# Docker named volumes / host bind-mounts may be owned by root,
# preventing the non-root sub2api user from writing files.
if [ "$(id -u)" = "0" ]; then
    mkdir -p /app/data
    # Use || true to avoid failure on read-only mounted files (e.g. config.yaml:ro)
    chown -R sub2api:sub2api /app/data 2>/dev/null || true
    # Only prepare the dedicated automatic-key volume. Never recursively chown
    # an operator-supplied secret path. A failure must not stop legacy platforms.
    if [ "${GATEWAY_CODEX4SERVER_ENABLED:-false}" = "true" ] &&
       [ "${GATEWAY_CODEX4SERVER_AUTO_GENERATE_SERVICE_KEY:-false}" = "true" ] &&
       [ "${GATEWAY_CODEX4SERVER_SERVICE_KEY_FILE:-}" = "/run/codex4server/service_key" ]; then
        if ! (mkdir -p /run/codex4server &&
              chown sub2api:sub2api /run/codex4server &&
              chmod 0700 /run/codex4server) 2>/dev/null; then
            printf '%s\n' 'Warning: cannot prepare Gateway service key directory; OpenAI Codex may be unavailable' >&2
        fi
    fi
    # Re-invoke this script as sub2api so the flag-detection below
    # also runs under the correct user.
    exec su-exec sub2api "$0" "$@"
fi

# Compatibility: if the first arg looks like a flag (e.g. --help),
# prepend the default binary so it behaves the same as the old
# ENTRYPOINT ["/app/sub2api"] style.
if [ "${1#-}" != "$1" ]; then
    set -- /app/sub2api "$@"
fi

exec "$@"
