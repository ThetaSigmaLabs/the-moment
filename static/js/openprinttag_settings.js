// Open Print Tag — Settings tab JS

// Human-readable labels and descriptions for each source_type.
var optSourceTypeInfo = {
    ofd_api: {
        label: 'OFD (Open Filament Database)',
        desc: 'Searches the Open Filament Database — a community-maintained registry of filament specs.'
    },
    filament_db_api: {
        label: 'filament-db (self-hosted)',
        desc: 'Searches a self-hosted filament-db REST API instance for local filament records.'
    }
};

function optSourceTypeLabel(sourceType) {
    var info = optSourceTypeInfo[sourceType];
    return info ? info.label : 'Unknown';
}

function optSourceTypeDesc(sourceType) {
    var info = optSourceTypeInfo[sourceType];
    return info ? info.desc : '';
}

function loadOPTSettings() {
    fetch('/api/openprinttag/sources')
        .then(function(r) { return r.json(); })
        .then(function(sources) { optRenderSourceList(sources); })
        .catch(function() {
            document.getElementById('opt-sources-list').innerHTML =
                '<p style="color:#ef4444;">Failed to load sources.</p>';
        });
}

function optRenderSourceList(sources) {
    var el = document.getElementById('opt-sources-list');
    if (!sources || sources.length === 0) {
        el.innerHTML = '<p style="color:var(--text-secondary);">No sources configured.</p>';
        return;
    }
    var rows = sources.map(function(s) {
        var statusBadge = s.enabled
            ? '<span style="color:#22c55e;font-weight:600;">Enabled</span>'
            : '<span style="color:var(--text-secondary);">Disabled</span>';
        var defaultBadge = s.is_default ? ' <span style="font-size:0.78em;color:var(--brand-light);">(default)</span>' : '';
        var testId = 'opt-test-result-' + s.id;
        return '<tr>' +
            '<td>' + optEscape(s.name) + defaultBadge + '</td>' +
            '<td style="font-size:0.82em;color:var(--text-secondary);">' + optEscape(s.url) + '</td>' +
            '<td title="' + optEscape(optSourceTypeDesc(s.source_type)) + '">' + optEscape(optSourceTypeLabel(s.source_type)) + '</td>' +
            '<td>' + statusBadge + '</td>' +
            '<td style="white-space:nowrap;">' +
                '<button class="btn btn-secondary" style="padding:3px 10px;font-size:0.82em;margin-right:4px;" ' +
                    'onclick="optToggleEnabled(' + s.id + ',' + (!s.enabled) + ',this);">' +
                    (s.enabled ? 'Disable' : 'Enable') + '</button>' +
                '<button class="btn btn-secondary" style="padding:3px 10px;font-size:0.82em;margin-right:4px;" ' +
                    'onclick="optTestSource(' + s.id + ',\'' + testId + '\');">Test</button>' +
                '<button class="btn btn-danger" style="padding:3px 10px;font-size:0.82em;" ' +
                    'onclick="optDeleteSource(' + s.id + ',\'' + optEscape(s.name) + '\');">Delete</button>' +
            '</td>' +
            '<td id="' + testId + '" style="font-size:0.82em;min-width:120px;"></td>' +
            '</tr>';
    }).join('');

    el.innerHTML = '<table style="width:100%;border-collapse:collapse;">' +
        '<thead><tr style="text-align:left;border-bottom:1px solid var(--border);">' +
        '<th style="padding:6px 8px;">Name</th>' +
        '<th style="padding:6px 8px;">URL</th>' +
        '<th style="padding:6px 8px;">Type</th>' +
        '<th style="padding:6px 8px;">Status</th>' +
        '<th style="padding:6px 8px;">Actions</th>' +
        '<th style="padding:6px 8px;"></th>' +
        '</tr></thead>' +
        '<tbody>' + rows + '</tbody></table>';
}

function optToggleEnabled(id, newEnabled, btn) {
    btn.disabled = true;
    fetch('/api/openprinttag/sources')
        .then(function(r) { return r.json(); })
        .then(function(sources) {
            var s = sources.find(function(x) { return x.id === id; });
            if (!s) return;
            s.enabled = newEnabled;
            return fetch('/api/openprinttag/sources/' + id, {
                method: 'PUT',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(s)
            });
        })
        .then(function() { loadOPTSettings(); })
        .catch(function(e) { btn.disabled = false; alert('Update failed: ' + e.message); });
}

function optTestSource(id, resultId) {
    var el = document.getElementById(resultId);
    if (el) el.innerHTML = '<span style="color:var(--text-secondary);">Testing…</span>';
    fetch('/api/openprinttag/sources/' + id + '/test', {method: 'POST'})
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!el) return;
            if (data.ok) {
                el.innerHTML = '<span style="color:#22c55e;">OK ' + data.latency_ms + 'ms</span>';
            } else {
                el.innerHTML = '<span style="color:#ef4444;" title="' + optEscape(data.error || '') + '">Failed</span>';
            }
        })
        .catch(function() {
            if (el) el.innerHTML = '<span style="color:#ef4444;">Error</span>';
        });
}

function optDeleteSource(id, name) {
    if (!confirm('Delete source "' + name + '"?')) return;
    fetch('/api/openprinttag/sources/' + id, {method: 'DELETE'})
        .then(function() { loadOPTSettings(); })
        .catch(function(e) { alert('Delete failed: ' + e.message); });
}

function optAddSource() {
    var name = document.getElementById('opt-add-name').value.trim();
    var url = document.getElementById('opt-add-url').value.trim();
    var type = document.getElementById('opt-add-type').value;
    if (!name || !url) { alert('Name and URL are required.'); return; }
    fetch('/api/openprinttag/sources', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({name: name, url: url, source_type: type, enabled: true})
    })
        .then(function(r) {
            if (!r.ok) return r.json().then(function(d) { throw new Error(d.error || r.statusText); });
            document.getElementById('opt-add-name').value = '';
            document.getElementById('opt-add-url').value = '';
            loadOPTSettings();
        })
        .catch(function(e) { alert('Add failed: ' + e.message); });
}

function optResetDefaults() {
    if (!confirm('Reset all sources to defaults? This will delete any custom sources.')) return;
    fetch('/api/openprinttag/sources/reset-defaults', {method: 'POST'})
        .then(function(r) { return r.json(); })
        .then(function(sources) { optRenderSourceList(sources); })
        .catch(function(e) { alert('Reset failed: ' + e.message); });
}

function optEscape(str) {
    return String(str || '')
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}
