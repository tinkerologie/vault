<?php
// control.php – AJAX endpoint for Vault service start/stop/status.
// Called by Vault.page JavaScript to manage the daemon without full page reload.

require_once __DIR__ . '/api.php';

header('Content-Type: application/json');

$RC = '/etc/rc.d/rc.vault';
$PIDFILE = '/var/run/vault.pid';
$CONFIG = '/boot/config/plugins/vault/vault.cfg';

// is_running consults the daemon's /api/v1/health endpoint first – this is the
// same authoritative check used at page load, so the post-action status agrees
// with what the user sees on refresh. The PID file is only used as a fallback
// when the health endpoint is unreachable (e.g. during shutdown), since a
// stale or non-readable PID file can mis-report state (issue #71).
function is_running() {
    global $PIDFILE;

    if (function_exists('vault_get')) {
        $health = @vault_get('/health');
        if (is_array($health) && (($health['status'] ?? '') === 'ok')) {
            return true;
        }
        // A definitive non-ok response means the daemon is not serving – fall
        // through to the PID check only when the HTTP call itself failed
        // (vault_get returns null on transport errors).
        if (is_array($health)) {
            return false;
        }
    }

    if (!file_exists($PIDFILE)) return false;
    $pid = trim(@file_get_contents($PIDFILE));
    if ($pid === '' || !ctype_digit($pid)) return false;
    return @posix_kill((int) $pid, 0);
}

$action = $_POST['action'] ?? $_GET['action'] ?? 'status';

// State-changing actions must be POST requests.
if ($action !== 'status' && $_SERVER['REQUEST_METHOD'] !== 'POST') {
    http_response_code(405);
    echo json_encode(['error' => 'State-changing actions require POST']);
    exit;
}

// Note: Unraid's web framework (emhttp/nginx) validates the csrf_token POST
// field at the gateway level for plugin pages and rejects requests with an
// invalid token before forwarding them to PHP. On success it also strips the
// csrf_token field from $_POST. Re-validating here would always fail (the
// stripped field is no longer visible) and produce a spurious 403 (issue
// observed when clicking Stop/Restart from Settings → Vault). The page-level
// auth and the gateway CSRF check together protect this endpoint.

// build_response shapes the JSON envelope returned for service-control actions
// so the frontend can distinguish a successful state change from a silent
// failure (issue #71).
function build_response(int $rc, array $out) {
    $running = is_running();
    $output = trim(implode("\n", $out));
    $resp = [
        'running' => $running,
        'success' => ($rc === 0),
        'exit_code' => (int) $rc,
    ];
    if ($rc !== 0) {
        $resp['error'] = $output !== '' ? $output : 'rc.vault exited with status ' . (int) $rc;
    }
    if ($output !== '') {
        $resp['output'] = $output;
    }
    return $resp;
}

switch ($action) {
    case 'start':
        $out = [];
        exec("$RC start 2>&1", $out, $rc);
        usleep(500000);
        echo json_encode(build_response($rc, $out));
        break;

    case 'stop':
        $out = [];
        exec("$RC stop 2>&1", $out, $rc);
        usleep(500000);
        echo json_encode(build_response($rc, $out));
        break;

    case 'restart':
        $out = [];
        exec("$RC restart 2>&1", $out, $rc);
        usleep(500000);
        echo json_encode(build_response($rc, $out));
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
