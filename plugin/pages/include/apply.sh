#!/bin/bash
# apply.sh — Called by Unraid's /update.php after saving vault.cfg.
# Restarts the Vault daemon to pick up port or bind-address changes (if it is running).

RC="/etc/rc.d/rc.vault"
PIDFILE="/var/run/vault.pid"

# Only restart if daemon is currently running.
if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    $RC restart
fi
