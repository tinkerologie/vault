const VAULT_API = '/plugins/vault/api.php';

async function vaultAPI(method, endpoint, data) {
    const opts = {
        method: method,
        headers: { 'Content-Type': 'application/json' },
    };
    if (data) opts.body = JSON.stringify(data);

    const resp = await fetch('http://127.0.0.1:28085/api/v1' + endpoint, opts);
    return resp.json();
}

function showModal(id) {
    document.getElementById(id).style.display = 'flex';
}

function hideModal(id) {
    document.getElementById(id).style.display = 'none';
}

// Kept in step with formatBytes in web/src/lib/utils.js and Bytes() in
// internal/format. This file is packaged into the release txz, so a size shown
// by these pages must read the same as one shown by the app or a notification.
// It previously stopped at TB with an unclamped index, so a petabyte rendered
// as "1 undefined".
function formatBytes(bytes) {
    if (bytes == null) return '0 B';
    if (!Number.isFinite(bytes)) return '\u2014';
    if (bytes === 0) return '0 B';
    if (bytes < 0) return '-' + formatBytes(-bytes);
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let i = 0;
    let v = bytes;
    while (v >= k && i < sizes.length - 1) { v /= k; i++; }
    if (i < sizes.length - 1 && Math.round(v * 10) / 10 >= k) { v /= k; i++; }
    if (i === 0) return Math.round(v) + ' ' + sizes[i];
    return parseFloat((Math.round(v * 10) / 10).toFixed(1)) + ' ' + sizes[i];
}

// WebSocket for real-time progress
let ws = null;

function connectWebSocket() {
    ws = new WebSocket('ws://127.0.0.1:28085/api/v1/ws');
    ws.onmessage = function(event) {
        const data = JSON.parse(event.data);
        if (data.type === 'progress') {
            updateProgress(data);
        }
    };
    ws.onclose = function() {
        setTimeout(connectWebSocket, 5000);
    };
}

function updateProgress(data) {
    const el = document.getElementById('progress-' + data.item);
    if (el) {
        el.style.width = data.percent + '%';
        el.textContent = data.message || (data.percent + '%');
    }
}

document.addEventListener('DOMContentLoaded', connectWebSocket);
