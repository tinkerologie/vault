<?php
require_once '/usr/local/emhttp/plugins/vault/include/api.php';

if (!isset($var) || !is_array($var)) {
    $var = @parse_ini_file('state/var.ini') ?: [];
}

$uiRoot = '/usr/local/emhttp/plugins/vault/ui';
$indexPath = $uiRoot . '/index.html';

if (!is_file($indexPath)) {
    http_response_code(503);
    header('Content-Type: text/html; charset=UTF-8');
    echo '<!doctype html><html><body><h1>Vault UI unavailable</h1><p>The web assets are missing. Redeploy or reinstall the plugin.</p></body></html>';
    exit;
}

$html = file_get_contents($indexPath);
if ($html === false) {
    http_response_code(500);
    header('Content-Type: text/html; charset=UTF-8');
    echo '<!doctype html><html><body><h1>Vault UI unavailable</h1><p>Failed to load the packaged web assets.</p></body></html>';
    exit;
}

$runtimeConfig = [
    'mode' => 'unraid-proxy',
    'proxyPath' => '/plugins/vault/include/proxy.php',
    'apiDisplayBase' => '/plugins/vault/include/proxy.php',
    'daemonBindAddress' => vault_get_bind_address(),
    'daemonPort' => vault_get_port(),
    'liveMode' => 'poll',
    'csrfToken' => $var['csrf_token'] ?? '',
    'timeFormat' => vault_detect_time_format(),
];

$inject = '<script>window.__VAULT_RUNTIME_CONFIG__=' . json_encode(
    $runtimeConfig,
    JSON_UNESCAPED_SLASHES | JSON_HEX_TAG | JSON_HEX_AMP | JSON_HEX_APOS | JSON_HEX_QUOT
) . ';</script>';

$html = strtr($html, [
    'href="/favicon.svg"' => 'href="/plugins/vault/ui/favicon.svg"',
    'href="./favicon.svg"' => 'href="/plugins/vault/ui/favicon.svg"',
    'src="/assets/' => 'src="/plugins/vault/ui/assets/',
    'src="./assets/' => 'src="/plugins/vault/ui/assets/',
    'href="/assets/' => 'href="/plugins/vault/ui/assets/',
    'href="./assets/' => 'href="/plugins/vault/ui/assets/',
]);

if (strpos($html, '</head>') !== false) {
    $html = str_replace('</head>', $inject . "\n</head>", $html);
} else {
    $html = $inject . $html;
}

header('Content-Type: text/html; charset=UTF-8');
echo $html;
