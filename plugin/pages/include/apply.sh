#!/bin/bash
# apply.sh — Called by Unraid's /update.php after saving vault.cfg.
# Restarts the Vault daemon to pick up port or bind-address changes (if it is running).

RC="/etc/rc.d/rc.vault"
PIDFILE="/var/run/vault.pid"
CONFIG="/boot/config/plugins/vault/vault.cfg"

# Safely read only the BIND_ADDRESS key from config (avoid sourcing arbitrary code).
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
fi

# Only restart if daemon is currently running.
if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    $RC restart
fi
