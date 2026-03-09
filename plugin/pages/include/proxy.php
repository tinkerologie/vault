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
if (!empty($_SERVER['HTTP_AUTHORIZATION'])) {
    $forwardHeaders[] = 'Authorization: ' . $_SERVER['HTTP_AUTHORIZATION'];
}
if (!empty($_SERVER['HTTP_X_API_KEY'])) {
    $forwardHeaders[] = 'X-API-Key: ' . $_SERVER['HTTP_X_API_KEY'];
}

$result = vault_http_request($forwardMethod, $path, $payload, $forwardHeaders);
if (!$result['ok']) {
    vault_proxy_error(502, 'vault daemon unavailable');
}

http_response_code($result['status'] > 0 ? $result['status'] : 502);
if (!empty($result['content_type'])) {
    header('Content-Type: ' . $result['content_type']);
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
