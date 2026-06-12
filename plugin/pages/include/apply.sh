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
BIND_ADDRESS=""

# /update.php runs this script from php-fpm, whose PATH may not include
# /usr/sbin where Unraid keeps `ip` (#136). Resolve an absolute path; if no
# binary is found we skip locality validation rather than wrongly resetting a
# valid NIC address to 127.0.0.1.
IP_BIN="$(command -v ip 2>/dev/null)"
if [ -z "$IP_BIN" ]; then
    for candidate in /usr/sbin/ip /sbin/ip; do
        if [ -x "$candidate" ]; then
            IP_BIN="$candidate"
            break
        fi
    done
fi

# Safely read only the keys we care about from config (avoid sourcing arbitrary
# code).
if [ -f "$CONFIG" ]; then
    BIND_ADDRESS="$(grep -E '^BIND_ADDRESS=' "$CONFIG" | head -1 | sed "s/^BIND_ADDRESS=//; s/^[\"']//; s/[\"'].*//")"
    case "${BIND_ADDRESS:-}" in
        127.0.0.1|0.0.0.0|::1|::|"") ;; # valid loopback/wildcard
        *)
            # Check if it's a local interface IP; if not, reset to 127.0.0.1.
            # Only validate when `ip` is available — a missing binary must not
            # masquerade as "address is not local".
            if [ -n "$IP_BIN" ] && \
               ! "$IP_BIN" addr show 2>/dev/null | grep -Fq "inet ${BIND_ADDRESS}/" && \
               ! "$IP_BIN" addr show 2>/dev/null | grep -Fq "inet6 ${BIND_ADDRESS}/"; then
                echo "Warning: bind address '${BIND_ADDRESS}' is not local; resetting to 127.0.0.1"
                if ! sed -i 's/^BIND_ADDRESS=.*/BIND_ADDRESS=127.0.0.1/' "$CONFIG"; then
                    echo "Error: failed to update BIND_ADDRESS in $CONFIG" >&2
                    exit 1
                fi
                BIND_ADDRESS="127.0.0.1"
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

# Probe host for the post-restart health check. The probe must target the
# address the daemon actually binds to — a hardcoded loopback probe fails when
# a specific NIC address is selected (issue #130). Wildcards are reachable via
# loopback; IPv6 literals need brackets in a URL.
PROBE_HOST="127.0.0.1"
case "${BIND_ADDRESS:-}" in
    0.0.0.0|::|"") PROBE_HOST="127.0.0.1" ;;
    *:*)           PROBE_HOST="[${BIND_ADDRESS}]" ;;
    *)             PROBE_HOST="$BIND_ADDRESS" ;;
esac

# Resolve the running daemon's PID. Mirrors rc.vault's resolve_pid: the PID
# file is authoritative, but if it is stale or missing fall back to pidof so a
# running daemon is still detected. Before this fallback a stale PID file made
# the restart silently skip, leaving the daemon on the old bind address/port
# until a manual restart (issue #130).
resolve_pid() {
    local pid=""
    if [ -f "$PIDFILE" ]; then
        pid="$(cat "$PIDFILE" 2>/dev/null)"
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            printf '%s\n' "$pid"
            return 0
        fi
    fi
    pid="$(pidof vault 2>/dev/null | awk '{print $1}')"
    if [ -n "$pid" ]; then
        printf '%s\n' "$pid"
        return 0
    fi
    return 1
}

# Only restart if the daemon is currently running.
if resolve_pid >/dev/null; then
    echo "Restarting Vault daemon to apply configuration (bind ${BIND_ADDRESS:-127.0.0.1}, port ${PORT})..."
    $RC restart

    # Verify the daemon actually came back up. The most common remaining
    # failure cause is a valid-but-already-in-use port: rc.vault removes the
    # PID file when the process dies, so a dead process is a definitive
    # failure signal. A process that stays alive but that we cannot reach over
    # HTTP is treated as success — the probe may be blocked even though the
    # daemon is healthy.
    attempts=6
    daemon_up=1
    while [ "$attempts" -gt 0 ]; do
        pid="$(cat "$PIDFILE" 2>/dev/null)"
        if [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
            daemon_up=0
            break
        fi
        if command -v curl >/dev/null 2>&1 && \
           curl -fsS -m 2 -o /dev/null "http://${PROBE_HOST}:${PORT}/api/v1/health" 2>/dev/null; then
            daemon_up=1
            break
        fi
        sleep 1
        attempts=$((attempts - 1))
    done

    if [ "$daemon_up" -eq 1 ]; then
        echo "Vault daemon restarted successfully (bind ${BIND_ADDRESS:-127.0.0.1}, port ${PORT})."
    else
        echo "ERROR: Vault daemon did not come back up after the configuration change."
        echo "Port ${PORT} may already be in use by another service. Last log lines:"
        tail -n 10 "$LOG" 2>/dev/null
    fi
else
    echo "Vault daemon is not running; configuration saved. It will apply on the next start."
fi
