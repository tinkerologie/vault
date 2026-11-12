<?php
// The native Unraid prepend verifies CSRF before this script runs and removes
// the submitted token. Only this fixed-action endpoint can sign a proof.

require_once __DIR__ . '/api.php';

header('Content-Type: application/json');
header('Cache-Control: no-store');
header('X-Content-Type-Options: nosniff');

function vault_restore_request_error(int $status, string $message): void {
    http_response_code($status);
    echo json_encode(['error' => $message]);
    exit;
}

if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'POST') {
    vault_restore_request_error(405, 'Restore Request actions require POST');
}

$scheme = isset($_SERVER['HTTPS']) ? 'https' : 'http';
$host = $_SERVER['HTTP_HOST'] ?? '';
$origin = $_SERVER['HTTP_ORIGIN'] ?? '';
$fetchSite = $_SERVER['HTTP_SEC_FETCH_SITE'] ?? 'same-origin';
if ($host === '' || $origin !== $scheme . '://' . $host || $fetchSite !== 'same-origin' ||
    isset($_SERVER['HTTP_X_API_KEY']) || isset($_SERVER['HTTP_AUTHORIZATION'])) {
    vault_restore_request_error(403, 'Open the signed-in Unraid Vault page to approve');
}

// Unraid 7.x local_prepend.php retains its verified token in its PHP scope
// while removing the raw POST field/header. Fail closed if that authority is
// unavailable. The native auth subrequest and this script use the same cookie.
$sessionCookie = session_name();
if (!isset($csrf_token) || !is_string($csrf_token) || $csrf_token === '' ||
    isset($_POST['csrf_token']) || isset($_SERVER['HTTP_X_CSRF_TOKEN']) ||
    !str_starts_with($sessionCookie, 'unraid_') ||
    !isset($_COOKIE[$sessionCookie]) ||
    (session_status() !== PHP_SESSION_ACTIVE &&
        !@session_start(['read_and_close' => true, 'use_strict_mode' => true, 'use_only_cookies' => true, 'cache_limiter' => ''])) ||
    !isset($_SESSION['unraid_login'])) {
    vault_restore_request_error(403, 'Open the signed-in Unraid Vault page to approve');
}
$operator = $_SESSION['unraid_user'] ?? '';
if (!is_string($operator) || strlen($operator) > 128 || preg_match('/[[:cntrl:]]/', $operator)) {
    $operator = '';
}
if (session_status() === PHP_SESSION_ACTIVE) {
    session_write_close();
}

$action = $_POST['action'] ?? '';
$id = $_POST['id'] ?? '';
$digest = $_POST['review_digest'] ?? '';
$passphrase = $_POST['passphrase'] ?? '';
if ($action !== 'approve' || !is_string($id) || preg_match('/\A[a-f0-9]{32}\z/', $id) !== 1 ||
    !is_string($digest) || preg_match('/\A[a-f0-9]{64}\z/', $digest) !== 1 ||
    !is_string($passphrase) || strlen($passphrase) > 1024) {
    vault_restore_request_error(400, 'Invalid Restore Request approval');
}

$keyPath = '/run/vault-operator/approval.key';
$directory = @lstat(dirname($keyPath));
$file = @lstat($keyPath);
if (!$directory || !$file || $directory['uid'] !== 0 || $directory['mode'] !== 0040700 ||
    $file['uid'] !== 0 || $file['mode'] !== 0100600 || $file['size'] !== 32) {
    vault_restore_request_error(503, 'Native Restore Request approval is unavailable');
}
$key = @file_get_contents($keyPath, false, null, 0, 32);
if (!is_string($key) || strlen($key) !== 32) {
    vault_restore_request_error(503, 'Native Restore Request approval is unavailable');
}

try {
    $payload = json_encode([
        'version' => 1,
        'action' => $action,
        'id' => $id,
        'review_digest' => $digest,
        'timestamp' => time(),
        'nonce' => bin2hex(random_bytes(16)),
        'operator' => $operator,
        'passphrase' => $passphrase,
    ], JSON_THROW_ON_ERROR);
    $envelope = json_encode(['payload' => $payload, 'signature' => hash_hmac('sha256', $payload, $key)], JSON_THROW_ON_ERROR);
} catch (Throwable $e) {
    vault_restore_request_error(400, 'Invalid Restore Request approval');
}
unset($key, $passphrase, $payload, $_POST['passphrase']);
set_time_limit(VAULT_HTTP_TIMEOUT_CONTENTS + 10);
$result = vault_http_request('POST', '/api/v1/restore-requests/' . $id . '/approve', $envelope);
if (!$result['ok']) {
    vault_restore_request_error(($result['errno'] ?? 0) === VAULT_CURLE_OPERATION_TIMEDOUT ? 504 : 502, 'Could not confirm approval with the Vault daemon');
}
http_response_code($result['status'] > 0 ? $result['status'] : 502);
echo $result['body'];
