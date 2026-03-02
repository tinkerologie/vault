<?php
// --- Config helpers ---

$VAULT_CFG = '/boot/config/plugins/vault/vault.cfg';

$VAULT_DEFAULTS = [
    'PORT' => '24085',
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
    return $cfg['PORT'] ?? '24085';
}

// --- API helpers ---

function vault_api($method, $endpoint, $data = null) {
    $port = vault_get_port();
    $url = "http://127.0.0.1:$port/api/v1" . $endpoint;
    $ch = curl_init($url);
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
    curl_setopt($ch, CURLOPT_CUSTOMREQUEST, strtoupper($method));
    curl_setopt($ch, CURLOPT_TIMEOUT, 5);
    if ($data !== null) {
        curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($data));
        curl_setopt($ch, CURLOPT_HTTPHEADER, ['Content-Type: application/json']);
    }
    $response = curl_exec($ch);
    $error = curl_error($ch);
    curl_close($ch);

    if ($error) {
        return null;
    }
    return json_decode($response, true);
}

function vault_get($endpoint) { return vault_api('GET', $endpoint); }
function vault_post($endpoint, $data = null) { return vault_api('POST', $endpoint, $data); }
function vault_put($endpoint, $data) { return vault_api('PUT', $endpoint, $data); }
function vault_delete($endpoint) { return vault_api('DELETE', $endpoint); }
?>
