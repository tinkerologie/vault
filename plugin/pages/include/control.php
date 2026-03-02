<?php
// control.php — AJAX endpoint for Vault service start/stop/status.
// Called by Vault.page JavaScript to manage the daemon without full page reload.

header('Content-Type: application/json');

$RC = '/etc/rc.d/rc.vault';
$PIDFILE = '/var/run/vault.pid';

function is_running() {
    global $PIDFILE;
    if (!file_exists($PIDFILE)) return false;
    $pid = trim(file_get_contents($PIDFILE));
    if (empty($pid)) return false;
    return posix_kill((int)$pid, 0);
}

$action = $_POST['action'] ?? $_GET['action'] ?? 'status';

switch ($action) {
    case 'start':
        exec("$RC start 2>&1", $out, $rc);
        // Brief wait for daemon to start.
        usleep(500000);
        echo json_encode(['running' => is_running(), 'output' => implode("\n", $out)]);
        break;

    case 'stop':
        exec("$RC stop 2>&1", $out, $rc);
        usleep(300000);
        echo json_encode(['running' => is_running(), 'output' => implode("\n", $out)]);
        break;

    case 'restart':
        exec("$RC restart 2>&1", $out, $rc);
        usleep(500000);
        echo json_encode(['running' => is_running(), 'output' => implode("\n", $out)]);
        break;

    case 'status':
        echo json_encode(['running' => is_running()]);
        break;

    default:
        http_response_code(400);
        echo json_encode(['error' => 'Invalid action']);
}
