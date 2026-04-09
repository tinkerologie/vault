<?php
// control.php — AJAX endpoint for Vault service start/stop/status.
// Called by Vault.page JavaScript to manage the daemon without full page reload.

header('Content-Type: application/json');

// Load Unraid state for CSRF validation.
$stateFile = '/var/local/emhttp/var.ini';
$var = file_exists($stateFile) ? @parse_ini_file($stateFile) : [];

$RC = '/etc/rc.d/rc.vault';
$PIDFILE = '/var/run/vault.pid';
$CONFIG = '/boot/config/plugins/vault/vault.cfg';

function is_running() {
    global $PIDFILE;
    if (!file_exists($PIDFILE)) return false;
    $pid = trim(file_get_contents($PIDFILE));
    if (empty($pid)) return false;
    return posix_kill((int)$pid, 0);
}

$action = $_POST['action'] ?? $_GET['action'] ?? 'status';

// State-changing actions must be POST requests.
if ($action !== 'status' && $_SERVER['REQUEST_METHOD'] !== 'POST') {
    http_response_code(405);
    echo json_encode(['error' => 'State-changing actions require POST']);
    exit;
}

// Validate CSRF token for state-changing actions (defense-in-depth).
if ($action !== 'status') {
    $csrf = $_POST['csrf_token'] ?? '';
    if (!isset($var['csrf_token']) || $csrf !== $var['csrf_token']) {
        http_response_code(403);
        echo json_encode(['error' => 'Invalid CSRF token']);
        exit;
    }
}

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

    case 'reset-config':
        // Reset vault.cfg to defaults, preserving SERVICE and SNAPSHOT_PATH.
        $service = 'yes';
        $snapshot = '';
        if (file_exists($CONFIG)) {
            $ini = @parse_ini_file($CONFIG, false, INI_SCANNER_RAW);
            if (is_array($ini)) {
                $service = $ini['SERVICE'] ?? 'yes';
                $snapshot = $ini['SNAPSHOT_PATH'] ?? '';
            }
        }
        // Constrain SERVICE to an allowlist.
        if (!in_array($service, ['yes', 'no'], true)) {
            $service = 'yes';
        }
        // Sanitize SNAPSHOT_PATH: only allow absolute paths with safe characters.
        // Reject leading dots to prevent hidden directory creation.
        $snapshot = preg_replace('/[^a-zA-Z0-9_\-\/. ]/', '', $snapshot);
        $snapshot = ltrim($snapshot, '.');
        // Write values in single quotes to prevent shell expansion when sourced.
        $content = "SERVICE='{$service}'\nPORT='24085'\nBIND_ADDRESS='127.0.0.1'\nSNAPSHOT_PATH='{$snapshot}'\n";
        $written = file_put_contents($CONFIG, $content, LOCK_EX);
        if ($written === false) {
            http_response_code(500);
            echo json_encode(['error' => 'Failed to write config file']);
            break;
        }
        // Restart daemon if running so it picks up the defaults.
        $was_running = is_running();
        $restart_ok = true;
        if ($was_running) {
            exec("$RC restart 2>&1", $out, $rc);
            usleep(500000);
            $restart_ok = ($rc === 0);
        }
        $response = ['running' => is_running(), 'reset' => true];
        if ($was_running && !$restart_ok) {
            $response['warning'] = 'Daemon restart may have failed';
        }
        echo json_encode($response);
        break;

    case 'status':
        echo json_encode(['running' => is_running()]);
        break;

    default:
        http_response_code(400);
        echo json_encode(['error' => 'Invalid action']);
}
