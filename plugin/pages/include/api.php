<?php
// --- Config helpers ---

$VAULT_CFG = '/boot/config/plugins/vault/vault.cfg';

$VAULT_DEFAULTS = [
    'PORT' => '24085',
    'BIND_ADDRESS' => '127.0.0.1',
];

function vault_load_config() {
    global $VAULT_CFG, $VAULT_DEFAULTS;
    $cfg = $VAULT_DEFAULTS;
    if (file_exists($VAULT_CFG)) {
        $ini = @parse_ini_file($VAULT_CFG, false, INI_SCANNER_RAW);
        if (is_array($ini)) {
            $cfg = array_merge($cfg, $ini);
        }
    }
    return $cfg;
}

function vault_get_port() {
    $cfg = vault_load_config();
    return trim((string) ($cfg['PORT'] ?? '24085'), " \t\n\r\0\x0B\"'");
}

function vault_get_bind_address() {
    $cfg = vault_load_config();
    $bind = trim((string) ($cfg['BIND_ADDRESS'] ?? '127.0.0.1'), " \t\n\r\0\x0B\"'");
    return $bind === '' ? '127.0.0.1' : $bind;
}

function vault_is_loopback_bind_address($bind = null) {
    $bind = trim((string) ($bind ?? vault_get_bind_address()));
    $normalized = strtolower(trim($bind, '[]'));
    return in_array($normalized, ['127.0.0.1', '::1', 'localhost'], true);
}

function vault_is_wildcard_bind_address($bind = null) {
    $bind = trim((string) ($bind ?? vault_get_bind_address()));
    $normalized = strtolower(trim($bind, '[]'));
    return $normalized === '0.0.0.0' || $normalized === '::';
}

function vault_detect_time_format() {
    $cfg = '/boot/config/plugins/dynamix/dynamix.cfg';
    if (!file_exists($cfg)) {
        return 'auto';
    }
    $ini = @parse_ini_file($cfg, true);
    if (!is_array($ini) || !isset($ini['display']['time'])) {
        return 'auto';
    }
    $fmt = (string) $ini['display']['time'];
    // PHP date() uppercase H or G indicate 24-hour format
    if (preg_match('/[HG]/', $fmt)) {
        return '24h';
    }
    return '12h';
}

function vault_http_host($host) {
    $host = trim((string) $host);
    if ($host === '') {
        return '127.0.0.1';
    }
    if (strpos($host, ':') !== false && $host[0] !== '[') {
        return '[' . $host . ']';
    }
    return $host;
}

function vault_target_url($path = '') {
    $port = vault_get_port();
    return 'http://127.0.0.1:' . $port . $path;
}

function vault_proxy_header() {
    return 'X-Vault-Proxy: unraid-plugin-proxy';
}

function vault_http_request($method, $path, $payload = null, $extraHeaders = []) {
    $ch = curl_init(vault_target_url($path));
    $headers = array_merge([vault_proxy_header()], $extraHeaders);
    $responseHeaders = [];

    curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
    curl_setopt($ch, CURLOPT_CUSTOMREQUEST, strtoupper($method));
    curl_setopt($ch, CURLOPT_TIMEOUT, 10);
    curl_setopt($ch, CURLOPT_FOLLOWLOCATION, false);
    curl_setopt($ch, CURLOPT_HEADERFUNCTION, static function($curl, $headerLine) use (&$responseHeaders) {
        $length = strlen($headerLine);
        $parts = explode(':', $headerLine, 2);
        if (count($parts) === 2) {
            $responseHeaders[strtolower(trim($parts[0]))] = trim($parts[1]);
        }
        return $length;
    });

    if ($payload !== null && $payload !== '') {
        curl_setopt($ch, CURLOPT_POSTFIELDS, $payload);
        $headers[] = 'Content-Type: application/json';
    }

    if (!empty($headers)) {
        curl_setopt($ch, CURLOPT_HTTPHEADER, $headers);
    }

    $body = curl_exec($ch);
    $error = curl_error($ch);
    $status = (int) curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
    $contentType = curl_getinfo($ch, CURLINFO_CONTENT_TYPE) ?: 'application/json';
    curl_close($ch);

    return [
        'ok' => $error === '',
        'status' => $status,
        'body' => $body === false ? '' : $body,
        'content_type' => $contentType,
        'headers' => $responseHeaders,
        'error' => $error,
    ];
}

// --- API helpers ---

function vault_api($method, $endpoint, $data = null) {
    $payload = $data === null ? null : json_encode($data);
    $result = vault_http_request($method, "/api/v1" . $endpoint, $payload, ['Accept: application/json']);
    if (!$result['ok']) {
        return null;
    }
    return json_decode($result['body'], true);
}

function vault_get($endpoint) { return vault_api('GET', $endpoint); }
function vault_post($endpoint, $data = null) { return vault_api('POST', $endpoint, $data); }
function vault_put($endpoint, $data) { return vault_api('PUT', $endpoint, $data); }
function vault_delete($endpoint) { return vault_api('DELETE', $endpoint); }
?>
