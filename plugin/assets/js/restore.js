async function loadRestorePoints(jobId) {
    if (!jobId) {
        document.getElementById('restorePointsTable').style.display = 'none';
        return;
    }

    const rps = await vaultAPI('GET', '/jobs/' + jobId + '/restore-points');
    const tbody = document.getElementById('restorePointsBody');
    tbody.innerHTML = '';

    if (rps && rps.length > 0) {
        rps.forEach(function(rp) {
            const tr = document.createElement('tr');

            const tdDate = document.createElement('td');
            tdDate.textContent = rp.created_at;
            tr.appendChild(tdDate);

            const tdType = document.createElement('td');
            tdType.textContent = rp.backup_type;
            tr.appendChild(tdType);

            const tdSize = document.createElement('td');
            tdSize.textContent = formatBytes(rp.size_bytes);
            tr.appendChild(tdSize);

            const tdAction = document.createElement('td');
            const btn = document.createElement('button');
            btn.className = 'vault-btn vault-btn-sm vault-btn-primary';
            btn.textContent = 'Restore';
            btn.addEventListener('click', function() { restoreFromPoint(rp.id); });
            tdAction.appendChild(btn);
            tr.appendChild(tdAction);

            tbody.appendChild(tr);
        });
        document.getElementById('restorePointsTable').style.display = 'table';
    } else {
        tbody.innerHTML = '<tr><td colspan="4">No restore points found</td></tr>';
        document.getElementById('restorePointsTable').style.display = 'table';
    }
}

function restoreFromPoint(rpId) {
    if (!confirm('Are you sure you want to restore from this point? This will overwrite current data.')) return;
    alert('Restore functionality will be implemented');
}
