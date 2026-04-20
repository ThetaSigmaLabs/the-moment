// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — Print History Tab

// ─── State ────────────────────────────────────────────────────────────────────

var _allHistory    = [];   // raw records from API
var _filteredRows  = [];   // after search + status filter
var _sortField     = 'print_finished';
var _sortAsc       = false;
var _activeEntry   = null; // record shown in modal

// ─── Load & Render Table ──────────────────────────────────────────────────────

function loadHistory() {
    fetch('/api/history?limit=500')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            _allHistory = data.records || [];
            filterHistory();
        })
        .catch(function(err) {
            document.getElementById('historyBody').innerHTML =
                '<tr><td colspan="9" style="text-align:center;padding:30px;color:#ef9a9a;">' +
                'Failed to load history: ' + err.message + '</td></tr>';
        });
}

function filterHistory() {
    var search = (document.getElementById('historySearch').value || '').toLowerCase();
    var status = document.getElementById('historyStatusFilter').value;

    _filteredRows = _allHistory.filter(function(r) {
        if (status && r.status !== status) return false;
        if (search) {
            var hay = (r.job_name + ' ' + r.printer_name + ' ' + (r.notes || '')).toLowerCase();
            if (!hay.includes(search)) return false;
        }
        return true;
    });

    sortHistory(_sortField, true); // re-sort then render
}

function sortHistory(field, skipToggle) {
    if (field === _sortField && !skipToggle) {
        _sortAsc = !_sortAsc;
    } else if (!skipToggle) {
        _sortField = field;
        _sortAsc = false; // newest first by default
    }
    _sortField = field;

    _filteredRows.sort(function(a, b) {
        var va = a[_sortField], vb = b[_sortField];
        if (typeof va === 'string') va = va.toLowerCase();
        if (typeof vb === 'string') vb = vb.toLowerCase();
        if (va < vb) return _sortAsc ? -1 : 1;
        if (va > vb) return _sortAsc ? 1  : -1;
        return 0;
    });

    renderTable();
}

function renderTable() {
    var tbody = document.getElementById('historyBody');
    var empty = document.getElementById('historyEmpty');
    var count = document.getElementById('historyCount');

    if (count) count.textContent = _filteredRows.length + ' record' + (_filteredRows.length !== 1 ? 's' : '');

    if (_filteredRows.length === 0) {
        tbody.innerHTML = '';
        if (empty) empty.style.display = 'block';
        return;
    }
    if (empty) empty.style.display = 'none';

    tbody.innerHTML = _filteredRows.map(function(r) {
        return buildRow(r);
    }).join('');
}

function buildRow(r) {
    var date     = _fmtDate(r.print_finished);
    var file     = _shortName(r.job_name);
    var usage    = r.filament_used > 0 ? r.filament_used.toFixed(1) + ' g' : '—';
    var time     = r.print_time_minutes > 0 ? _fmtMin(r.print_time_minutes) : '—';
    var cost     = r.total_cost > 0 ? _fmtCost(r.total_cost, r.currency) : '—';
    var note     = r.notes ? _esc(r.notes.substring(0, 40)) + (r.notes.length > 40 ? '…' : '') : '';
    var statusBadge = _statusBadge(r.status);
    var thumbCell = r.thumbnail_base64
        ? '<img src="' + _esc(r.thumbnail_base64) + '" style="width:40px;height:40px;' +
          'object-fit:cover;border-radius:4px;border:1px solid #333;display:block;margin:auto;">'
        : '<span style="color:#444;font-size:1.2em;">·</span>';

    return '<tr onclick="openHistoryModal(' + r.id + ')" ' +
           'style="border-bottom:1px solid #2a2a2a;cursor:pointer;transition:background 0.15s;" ' +
           'onmouseover="this.style.background=\'rgba(255,255,255,0.04)\'" ' +
           'onmouseout="this.style.background=\'\'">' +
           '<td style="padding:9px 12px;white-space:nowrap;color:#aaa;">' + date + '</td>' +
           '<td style="padding:9px 12px;white-space:nowrap;">' + _esc(r.printer_name) + '</td>' +
           '<td style="padding:9px 12px;max-width:240px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="' + _esc(r.job_name) + '">' + _esc(file) + '</td>' +
           '<td style="padding:9px 12px;text-align:right;white-space:nowrap;">' + usage + '</td>' +
           '<td style="padding:9px 12px;text-align:right;white-space:nowrap;color:#aaa;">' + time + '</td>' +
           '<td style="padding:9px 12px;text-align:right;white-space:nowrap;' + (r.total_cost > 0 ? 'color:#c8b8ff;' : 'color:#555;') + '">' + cost + '</td>' +
           '<td style="padding:9px 12px;color:#888;font-size:0.85em;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">' + note + '</td>' +
           '<td style="padding:9px 12px;text-align:center;">' + statusBadge + '</td>' +
           '<td style="padding:9px 12px;text-align:center;">' + thumbCell + '</td>' +
           '</tr>';
}

// ─── History Detail Modal ─────────────────────────────────────────────────────

function openHistoryModal(id) {
    fetch('/api/history/' + id)
        .then(function(r) { return r.json(); })
        .then(function(record) {
            _activeEntry = record;
            populateModal(record);
            document.getElementById('historyDetailModal').style.display = 'block';
        })
        .catch(function(err) {
            alert('Failed to load record: ' + err.message);
        });
}

function populateModal(r) {
    // Title
    document.getElementById('historyDetailTitle').textContent = r.job_name || 'Print Detail';

    // Thumbnail
    var thumbEl = document.getElementById('historyThumb');
    if (r.thumbnail_base64 && r.thumbnail_base64.startsWith('data:')) {
        thumbEl.innerHTML = '<img src="' + r.thumbnail_base64 + '" ' +
            'style="width:120px;height:120px;object-fit:cover;border-radius:8px;">';
    } else {
        thumbEl.innerHTML = '<span style="color:#444;font-size:2.5em;">🖼</span>';
    }

    // Meta rows
    var rows = [
        ['Printer',    r.printer_name],
        ['Toolhead',   'T' + r.toolhead_id],
        ['Spool ID',   r.spool_id > 0 ? '#' + r.spool_id : '—'],
        ['Filament',   r.filament_used > 0 ? r.filament_used.toFixed(2) + ' g' : '—'],
        ['Print time', r.print_time_minutes > 0 ? _fmtMin(r.print_time_minutes) : '—'],
        ['Finished',   _fmtDateFull(r.print_finished)],
        ['Status',     _statusBadge(r.status)],
    ];
    if (r.total_cost > 0) {
        rows.push(['Total cost', '<strong style="color:#c8b8ff;">' + _fmtCost(r.total_cost, r.currency) + '</strong>']);
    }

    var metaHTML = rows.map(function(row) {
        return '<tr>' +
            '<td style="padding:5px 12px 5px 0;color:#888;white-space:nowrap;vertical-align:top;">' + row[0] + '</td>' +
            '<td style="padding:5px 0;word-break:break-all;">' + row[1] + '</td>' +
            '</tr>';
    }).join('');
    document.getElementById('historyMetaRows').innerHTML = metaHTML;

    // Cost breakdown
    var costSection = document.getElementById('historyDetailCost');
    var recalcBtn   = document.getElementById('historyRecalcBtn');
    if (r.total_cost > 0) {
        costSection.style.display = 'block';
        document.getElementById('historyDetailCostRows').innerHTML =
            '<p style="color:#aaa;font-size:0.85em;margin:0;">' +
            'Stored total: ' + _fmtCost(r.total_cost, r.currency) + '</p>';
        if (recalcBtn) recalcBtn.style.display = '';
    } else {
        costSection.style.display = 'none';
        if (recalcBtn) recalcBtn.style.display = r.filament_used > 0 ? '' : 'none';
    }

    // Note
    document.getElementById('historyNoteInput').value = r.notes || '';
}

function closeHistoryModal() {
    document.getElementById('historyDetailModal').style.display = 'none';
    _activeEntry = null;
}

function saveHistoryNote() {
    if (!_activeEntry) return;
    var note = document.getElementById('historyNoteInput').value;
    fetch('/api/history/' + _activeEntry.id + '/note', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ note: note })
    })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { alert('Error: ' + data.error); return; }
            _activeEntry.notes = note;
            // Update the row in the table without full reload
            var idx = _allHistory.findIndex(function(r) { return r.id === _activeEntry.id; });
            if (idx >= 0) _allHistory[idx].notes = note;
            renderTable();
            closeHistoryModal();
        })
        .catch(function(err) { alert('Error: ' + err.message); });
}

function deleteHistoryEntry() {
    if (!_activeEntry) return;
    if (!confirm('Delete this print history record?\nThis cannot be undone.')) return;
    fetch('/api/history/' + _activeEntry.id, { method: 'DELETE' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { alert('Error: ' + data.error); return; }
            _allHistory = _allHistory.filter(function(r) { return r.id !== _activeEntry.id; });
            filterHistory();
            closeHistoryModal();
        })
        .catch(function(err) { alert('Error: ' + err.message); });
}

// Re-calculate cost from stored filament + time using current cost settings
function recalcHistoryCost() {
    if (!_activeEntry) return;
    fetch('/api/cost/calculate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            filament_grams: _activeEntry.filament_used,
            print_time_min: _activeEntry.print_time_minutes,
            spool_id:       _activeEntry.spool_id
        })
    })
        .then(function(r) { return r.json(); })
        .then(function(bd) {
            if (bd.error) { alert('Error: ' + bd.error); return; }
            var costSection = document.getElementById('historyDetailCost');
            costSection.style.display = 'block';
            // Use _renderCostRows from cost-calculator.js
            if (typeof _renderCostRows === 'function') {
                document.getElementById('historyDetailCostRows').innerHTML = _renderCostRows(bd, bd.currency);
            } else {
                document.getElementById('historyDetailCostRows').innerHTML =
                    '<p>Total: ' + _fmtCost(bd.total_cost, bd.currency) + '</p>';
            }
            // Update the in-memory record
            _activeEntry.total_cost = bd.total_cost;
            _activeEntry.currency   = bd.currency;
            var idx = _allHistory.findIndex(function(r) { return r.id === _activeEntry.id; });
            if (idx >= 0) { _allHistory[idx].total_cost = bd.total_cost; }
            renderTable();
        })
        .catch(function(err) { alert('Error: ' + err.message); });
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function _fmtDate(iso) {
    if (!iso) return '—';
    var d = new Date(iso);
    if (isNaN(d)) return iso;
    return d.toLocaleDateString() + ' ' +
           d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function _fmtDateFull(iso) {
    if (!iso) return '—';
    var d = new Date(iso);
    return isNaN(d) ? iso : d.toLocaleString();
}

function _fmtMin(min) {
    if (!min || min <= 0) return '—';
    var h = Math.floor(min / 60), m = Math.round(min % 60);
    return h > 0 ? h + 'h ' + m + 'm' : m + ' min';
}

function _fmtCost(amount, currency) {
    try {
        return new Intl.NumberFormat('en-US', {
            style: 'currency', currency: currency || 'USD', minimumFractionDigits: 2
        }).format(amount);
    } catch(e) {
        return (currency || '$') + amount.toFixed(2);
    }
}

function _shortName(path) {
    if (!path) return '—';
    var parts = path.split(/[/\\]/);
    return parts[parts.length - 1];
}

function _esc(s) {
    if (!s) return '';
    return String(s)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}

function _statusBadge(status) {
    var styles = {
        completed: 'background:#1b4332;color:#6ee7a0;',
        cancelled: 'background:#3d2a00;color:#ffb347;',
        failed:    'background:#3d0000;color:#ff7070;'
    };
    var s = status || 'completed';
    var style = styles[s] || styles.completed;
    return '<span style="' + style + 'padding:2px 8px;border-radius:10px;font-size:0.8em;' +
           'white-space:nowrap;">' + s + '</span>';
}

// ─── Init ─────────────────────────────────────────────────────────────────────

// Load when tab is first clicked
document.addEventListener('DOMContentLoaded', function() {
    // Hook into tab switching
    var tabs = document.querySelectorAll('.tab');
    tabs.forEach(function(btn) {
        btn.addEventListener('click', function() {
            if (btn.getAttribute('onclick') && btn.getAttribute('onclick').includes('history')) {
                if (_allHistory.length === 0) loadHistory();
            }
        });
    });

    // Close modal on outside click
    window.addEventListener('click', function(e) {
        var m = document.getElementById('historyDetailModal');
        if (m && e.target === m) closeHistoryModal();
    });
});
