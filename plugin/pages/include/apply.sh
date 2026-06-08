#!/bin/bash
# apply.sh — Called by Unraid's /update.php after saving vault.cfg.
# Validates the saved port and bind address, then restarts the Vault daemon to
# pick up the changes (if it is running) and verifies it actually came back up
# (issue #124 — an invalid or already-in-use port used to leave the daemon
# silently dead, making the web UI unreachable with no feedback).

RC="/etc/rc.d/rc.vault"
PIDFILE="/var/run/vault.pid"
CONFIG="/boot/config/plugins/vault/vault.cfg"
LOG="/var/log/vault.log"
DEFAULT_PORT=24085

PORT="$DEFAULT_PORT"

# Safely read only the keys we care about from config (avoid sourcing arbitrary
# code).
if [ -f "$CONFIG" ]; then
    BIND_ADDRESS="$(grep -E '^BIND_ADDRESS=' "$CONFIG" | head -1 | sed "s/^BIND_ADDRESS=//; s/^[\"']//; s/[\"'].*//")"
    case "${BIND_ADDRESS:-}" in
        127.0.0.1|0.0.0.0|::1|::|"") ;; # valid loopback/wildcard
        *)
            # Check if it's a local interface IP; if not, reset to 127.0.0.1.
            if ! ip addr show 2>/dev/null | grep -Fq "inet ${BIND_ADDRESS}/" && \
               ! ip addr show 2>/dev/null | grep -Fq "inet6 ${BIND_ADDRESS}/"; then
                echo "Warning: bind address '${BIND_ADDRESS}' is not local; resetting to 127.0.0.1"
                if ! sed -i 's/^BIND_ADDRESS=.*/BIND_ADDRESS=127.0.0.1/' "$CONFIG"; then
                    echo "Error: failed to update BIND_ADDRESS in $CONFIG" >&2
                    exit 1
                fi
            fi
            ;;
    esac

    # Validate the port server-side. The HTML form enforces 1024-65535, but
    # that constraint is client-side only — an out-of-range or non-numeric
    # value written straight to vault.cfg would make the daemon fail to bind
    # and exit, which is the root cause of issue #124. Reset to the default on
    # anything invalid so the daemon always has a usable port to start on.
    PORT_RAW="$(grep -E '^PORT=' "$CONFIG" | head -1 | sed "s/^PORT=//; s/^[\"']//; s/[\"'].*//")"
    if [ -z "$PORT_RAW" ]; then
        PORT="$DEFAULT_PORT"
    elif echo "$PORT_RAW" | grep -Eq '^[0-9]+$' && [ "$PORT_RAW" -ge 1024 ] && [ "$PORT_RAW" -le 65535 ]; then
        PORT="$PORT_RAW"
    else
        echo "Warning: port '${PORT_RAW}' is invalid (must be a number 1024-65535); resetting to ${DEFAULT_PORT}"
        if ! sed -i "s/^PORT=.*/PORT=${DEFAULT_PORT}/" "$CONFIG"; then
            echo "Error: failed to update PORT in $CONFIG" >&2
            exit 1
        fi
        PORT="$DEFAULT_PORT"
    fi
fi

# Only restart if the daemon is currently running.
if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    $RC restart

    # Verify the daemon actually came back up. The most common remaining
    # failure cause is a valid-but-already-in-use port: rc.vault removes the
    # PID file when the process dies, so a dead process is a definitive
    # failure signal. A process that stays alive but that we cannot reach over
    # HTTP is treated as success — it may be bound to a non-loopback address
    # this loopback probe cannot hit.
    attempts=6
    daemon_up=1
    while [ "$attempts" -gt 0 ]; do
        pid="$(cat "$PIDFILE" 2>/dev/null)"
        if [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
            daemon_up=0
            break
        fi
        if command -v curl >/dev/null 2>&1 && \
           curl -fsS -m 2 -o /dev/null "http://127.0.0.1:${PORT}/api/v1/health" 2>/dev/null; then
            daemon_up=1
            break
        fi
        sleep 1
        attempts=$((attempts - 1))
    done

    if [ "$daemon_up" -eq 1 ]; then
        echo "Vault daemon restarted successfully on port ${PORT}."
    else
        echo "ERROR: Vault daemon did not come back up after the configuration change."
        echo "Port ${PORT} may already be in use by another service. Last log lines:"
        tail -n 10 "$LOG" 2>/dev/null
    fi
fi
