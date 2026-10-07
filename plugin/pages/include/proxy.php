<?php
require_once '/usr/local/emhttp/plugins/vault/include/api.php';

function vault_proxy_error($status, $message) {
    http_response_code($status);
    header('Content-Type: application/json');
    echo json_encode(['error' => $message]);
    exit;
}

$path = $_GET['path'] ?? $_POST['path'] ?? '';
if (!is_string($path) || $path === '' || $path[0] !== '/') {
    vault_proxy_error(400, 'missing or invalid proxy path');
}

if ($path !== '/api/v1' && strpos($path, '/api/v1/') !== 0) {
    vault_proxy_error(403, 'only /api/v1 routes can be proxied');
}

if ($path === '/api/v1/ws' || strpos($path, '/api/v1/ws?') === 0) {
    vault_proxy_error(501, 'websocket proxy unavailable; plugin mode uses polling');
}

$requestMethod = strtoupper($_SERVER['REQUEST_METHOD'] ?? 'GET');
$forwardMethod = $requestMethod;
$payload = null;

if ($requestMethod === 'POST') {
    $forwardMethod = strtoupper($_POST['method'] ?? 'POST');
    if (isset($_POST['payload']) && $_POST['payload'] !== '') {
        $payload = $_POST['payload'];
    }
}

if (!in_array($forwardMethod, ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE'], true)) {
    vault_proxy_error(405, 'unsupported proxy method');
}

$forwardHeaders = ['Accept: application/json'];

// Keep PHP's execution limit above the contents cURL budget so a slow remote
// listing is not killed before cURL returns (issue #449).
if (vault_is_long_request($forwardMethod, $path)) {
    set_time_limit(VAULT_HTTP_TIMEOUT_CONTENTS + 10);
}

$result = vault_http_request($forwardMethod, $path, $payload, $forwardHeaders);
if (!$result['ok']) {
    // A timeout means the daemon is up but slow; don't report it as down.
    if (($result['errno'] ?? 0) === VAULT_CURLE_OPERATION_TIMEDOUT) {
        vault_proxy_error(504, 'vault daemon request timed out');
    }
    vault_proxy_error(502, 'vault daemon unavailable');
}

http_response_code($result['status'] > 0 ? $result['status'] : 502);

// Security headers to prevent XSS and MIME-type sniffing.
header('X-Content-Type-Options: nosniff');

if (!empty($result['content_type'])) {
    header('Content-Type: ' . $result['content_type']);
} else {
    // Default to JSON to prevent browser HTML interpretation.
    header('Content-Type: application/json');
}
if (!empty($result['headers']['content-disposition'])) {
    header('Content-Disposition: ' . $result['headers']['content-disposition']);
}
if (!empty($result['headers']['cache-control'])) {
    header('Cache-Control: ' . $result['headers']['cache-control']);
}

if ($forwardMethod !== 'HEAD') {
    echo $result['body'];
}
