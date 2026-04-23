// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — Print History Tab

// ─── State ────────────────────────────────────────────────────────────────────

var _allSessions      = [];   // raw PrintSession objects from /api/sessions
var _filteredSessions = [];
var _sortField        = 'print_finished';
var _sortAsc          = false;
var _activeEntry      = null;
var _expandedSessions = {};   // session key → true when expanded

// ─── Load & Render ────────────────────────────────────────────────────────────

function loadHistory() {
    fetch('/api/sessions?limit=500')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            _allSessions = data.sessions || [];
            filterHistory();
        })
        .catch(function(err) {
            document.getElementById('historyBody').innerHTML =
                '<tr><td colspan="10" style="text-align:center;padding:30px;color:#ef9a9a;">' +
                'Failed to load history: ' + err.message + '</td></tr>';
        });
}

function filterHistory() {
    var search = (document.getElementById('historySearch').value || '').toLowerCase();
    var status = document.getElementById('historyStatusFilter').value;

    _filteredSessions = _allSessions.filter(function(s) {
        if (status && s.status !== status) return false;
        if (search) {
            var hay = (s.job_name + ' ' + s.printer_name).toLowerCase();
            if (!hay.includes(search)) {
                // also check individual records for notes
                var recMatch = (s.records || []).some(function(r) {
                    return (r.job_name + ' ' + r.printer_name + ' ' + (r.notes || '')).toLowerCase().includes(search);
                });
                if (!recMatch) return false;
            }
        }
        return true;
    });

    sortHistory(_sortField, true);
}

function sortHistory(field, skipToggle) {
    if (field === _sortField && !skipToggle) {
        _sortAsc = !_sortAsc;
    } else if (!skipToggle) {
        _sortField = field;
        _sortAsc = false;
    }
    _sortField = field;

    _filteredSessions.sort(function(a, b) {
        var va = _sessionSortValue(a, _sortField);
        var vb = _sessionSortValue(b, _sortField);
        if (typeof va === 'string') va = va.toLowerCase();
        if (typeof vb === 'string') vb = vb.toLowerCase();
        if (va < vb) return _sortAsc ? -1 : 1;
        if (va > vb) return _sortAsc ? 1  : -1;
        return 0;
    });

    renderTable();
}

function _sessionSortValue(s, field) {
    switch (field) {
        case 'print_finished':   return s.print_finished || '';
        case 'printer_name':     return s.printer_name || '';
        case 'filament_used':    return s.total_filament_grams || 0;
        case 'total_cost':       return s.total_cost || 0;
        default:                 return '';
    }
}

function renderTable() {
    var tbody  = document.getElementById('historyBody');
    var empty  = document.getElementById('historyEmpty');
    var count  = document.getElementById('historyCount');

    var totalRecords = _filteredSessions.reduce(function(n, s) {
        return n + (s.tool_count || 1);
    }, 0);
    if (count) {
        var n = _filteredSessions.length;
        var label = n + ' session' + (n !== 1 ? 's' : '');
        if (totalRecords !== n) label += ' (' + totalRecords + ' toolheads)';
        count.textContent = label;
    }

    if (_filteredSessions.length === 0) {
        tbody.innerHTML = '';
        if (empty) empty.style.display = 'block';
        return;
    }
    if (empty) empty.style.display = 'none';

    var html = '';
    _filteredSessions.forEach(function(s, i) {
        var key    = s.session_id || ('__solo_' + i);
        var multi  = s.tool_count > 1;
        var expanded = !!_expandedSessions[key];
        html += buildSessionRow(s, i, key, multi, expanded);
        if (multi && expanded) {
            (s.records || []).forEach(function(r) {
                html += buildSubRow(r);
            });
        }
    });
    tbody.innerHTML = html;
}

// ─── Row builders ─────────────────────────────────────────────────────────────

function buildSessionRow(s, i, key, multi, expanded) {
    var date   = _fmtDate(s.print_finished);
    var usage  = s.total_filament_grams > 0 ? s.total_filament_grams.toFixed(1) + ' g' : '—';
    var time   = _timeFromSession(s);
    var cost   = s.total_cost > 0 ? _fmtCost(s.total_cost, s.currency) : '—';
    var statusBadge = _statusBadge(s.status);
    var sourceBadge = _sourceBadge(s.source);

    // Thumbnail: use first record's thumbnail if available
    var thumbSrc = '';
    if (s.records && s.records.length > 0) {
        for (var ri = 0; ri < s.records.length; ri++) {
            if (s.records[ri].thumbnail_base64) { thumbSrc = s.records[ri].thumbnail_base64; break; }
        }
    }
    var thumbCell = thumbSrc
        ? '<img src="' + _esc(thumbSrc) + '" style="width:40px;height:40px;object-fit:cover;border-radius:4px;border:1px solid #333;display:block;margin:auto;">'
        : '<span style="color:#444;font-size:1.2em;">·</span>';

    // File cell: expand arrow for multi-toolhead, source badge, file name
    var expandIcon = '';
    var toolBadge  = '';
    if (multi) {
        expandIcon = '<span style="display:inline-block;width:16px;color:#888;font-size:0.75em;transition:transform 0.15s;' +
            (expanded ? 'transform:rotate(90deg);' : '') + '">' +
            (expanded ? '▼' : '▶') + '</span> ';
        toolBadge = '<span style="margin-left:6px;background:#1a3a5c;color:#7ab8f5;padding:1px 6px;' +
            'border-radius:8px;font-size:0.72em;white-space:nowrap;">' + s.tool_count + ' tools</span>';
    }
    var file = _shortName(s.job_name);
    var fileCell = expandIcon + _esc(file) + toolBadge;

    // Note: aggregate — show first record's note if any
    var note = '';
    if (s.records && s.records[0] && s.records[0].notes) {
        var n = s.records[0].notes;
        note = _esc(n.substring(0, 40)) + (n.length > 40 ? '…' : '');
    }

    var onclick = multi
        ? 'toggleSession(\'' + _esc(key) + '\', ' + i + ')'
        : (s.records && s.records[0] ? 'openHistoryModal(' + s.records[0].id + ')' : '');

    var rowStyle = 'border-bottom:1px solid #2a2a2a;cursor:pointer;transition:background 0.15s;' +
        (multi ? 'border-left:3px solid #1a3a5c;' : '');

    return '<tr onclick="' + onclick + '" ' +
        'style="' + rowStyle + '" ' +
        'onmouseover="this.style.background=\'rgba(255,255,255,0.04)\'" ' +
        'onmouseout="this.style.background=\'\'">' +
        '<td style="padding:9px 12px;white-space:nowrap;color:#aaa;">' + date + '</td>' +
        '<td style="padding:9px 12px;white-space:nowrap;">' + _esc(s.printer_name) + sourceBadge + '</td>' +
        '<td style="padding:9px 12px;max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="' + _esc(s.job_name) + '">' + fileCell + '</td>' +
        '<td style="padding:9px 12px;text-align:right;white-space:nowrap;">' + usage + '</td>' +
        '<td style="padding:9px 12px;text-align:right;white-space:nowrap;color:#aaa;">' + time + '</td>' +
        '<td style="padding:9px 12px;text-align:right;white-space:nowrap;' + (s.total_cost > 0 ? 'color:#c8b8ff;' : 'color:#555;') + '">' + cost + '</td>' +
        '<td style="padding:9px 12px;color:#888;font-size:0.85em;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">' + note + '</td>' +
        '<td style="padding:9px 12px;text-align:center;">' + statusBadge + '</td>' +
        '<td style="padding:9px 12px;text-align:center;">' + thumbCell + '</td>' +
        '</tr>';
}

function buildSubRow(r) {
    var usage = r.filament_used > 0 ? r.filament_used.toFixed(1) + ' g' : '—';
    var time  = r.print_time_minutes > 0 ? _fmtMin(r.print_time_minutes) : '—';
    var cost  = r.total_cost > 0 ? _fmtCost(r.total_cost, r.currency) : '—';

    return '<tr onclick="openHistoryModal(' + r.id + ')" ' +
        'style="border-bottom:1px solid #222;cursor:pointer;background:rgba(0,0,0,0.25);" ' +
        'onmouseover="this.style.background=\'rgba(255,255,255,0.03)\'" ' +
        'onmouseout="this.style.background=\'rgba(0,0,0,0.25)\'">' +
        '<td style="padding:6px 12px;color:#666;font-size:0.82em;"></td>' +
        '<td style="padding:6px 12px;color:#777;font-size:0.82em;padding-left:28px;">T' + r.toolhead_id + ' · spool&nbsp;' + (r.spool_id > 0 ? '#' + r.spool_id : '—') + '</td>' +
        '<td style="padding:6px 12px;color:#666;font-size:0.82em;padding-left:28px;">' + _esc(_shortName(r.job_name)) + '</td>' +
        '<td style="padding:6px 12px;text-align:right;color:#aaa;font-size:0.82em;">' + usage + '</td>' +
        '<td style="padding:6px 12px;text-align:right;color:#666;font-size:0.82em;">' + time + '</td>' +
        '<td style="padding:6px 12px;text-align:right;color:#666;font-size:0.82em;">' + cost + '</td>' +
        '<td style="padding:6px 12px;font-size:0.82em;color:#555;">' + _esc((r.notes || '').substring(0, 40)) + '</td>' +
        '<td style="padding:6px 12px;text-align:center;">' + _statusBadge(r.status) + '</td>' +
        '<td style="padding:6px 12px;text-align:center;">' +
        (r.thumbnail_base64 ? '<img src="' + _esc(r.thumbnail_base64) + '" style="width:30px;height:30px;object-fit:cover;border-radius:3px;display:block;margin:auto;">' : '') +
        '</td>' +
        '</tr>';
}

function toggleSession(key, idx) {
    _expandedSessions[key] = !_expandedSessions[key];
    renderTable();
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
    document.getElementById('historyDetailTitle').textContent = r.job_name || 'Print Detail';

    var thumbEl = document.getElementById('historyThumb');
    if (r.thumbnail_base64 && r.thumbnail_base64.startsWith('data:')) {
        thumbEl.innerHTML = '<img src="' + r.thumbnail_base64 + '" ' +
            'style="width:120px;height:120px;object-fit:cover;border-radius:8px;">';
    } else {
        thumbEl.innerHTML = '<span style="color:#444;font-size:2.5em;">🖼</span>';
    }

    var rows = [
        ['Printer',    r.printer_name],
        ['Source',     _sourceLabel(r.source)],
        ['Toolhead',   'T' + r.toolhead_id],
        ['Spool ID',   r.spool_id > 0 ? '#' + r.spool_id : '—'],
        ['Filament',   r.filament_used > 0 ? r.filament_used.toFixed(2) + ' g' : '—'],
        ['Print time', r.print_time_minutes > 0 ? _fmtMin(r.print_time_minutes) : '—'],
        ['Finished',   _fmtDateFull(r.print_finished)],
        ['Status',     _statusBadge(r.status)],
    ];
    if (r.session_id) {
        rows.push(['Session', '<span style="font-size:0.75em;color:#666;word-break:break-all;">' + _esc(r.session_id) + '</span>']);
    }
    if (r.total_cost > 0) {
        rows.push(['Total cost', '<strong style="color:#c8b8ff;">' + _fmtCost(r.total_cost, r.currency) + '</strong>']);
    }

    // OctoPrint-specific precision and timing details
    if (r.source === 'octoprint') {
        if (r.total_duration_sec > 0) {
            rows.push(['Total time',  _fmtSec(r.total_duration_sec)]);
            rows.push(['Print time',  _fmtSec(r.print_duration_sec)]);
            if (r.pause_count > 0) {
                rows.push(['Pauses', r.pause_count + ' (' + _fmtSec(r.pause_duration_sec) + ')']);
            }
        }
        rows.push(['Time data',     r.time_precision === 'exact' ? '✓ Exact' : 'Approximate']);
        rows.push(['Filament data', r.filament_precision === 'measured' ? '✓ Measured' : 'Estimated']);
        if (r.cancel_reason) rows.push(['Cancel reason', r.cancel_reason]);
    }

    document.getElementById('historyMetaRows').innerHTML = rows.map(function(row) {
        return '<tr><td style="padding:5px 12px 5px 0;color:#888;white-space:nowrap;vertical-align:top;">' + row[0] +
            '</td><td style="padding:5px 0;word-break:break-all;">' + row[1] + '</td></tr>';
    }).join('');

    // Filament usages (OctoPrint multi-spool / multi-tool detail)
    var fuSection = document.getElementById('historyFilamentUsages');
    if (fuSection) {
        if (r.filament_usages && r.filament_usages.length > 1) {
            var fuHTML = '<div style="margin-top:12px;"><div style="color:#888;font-size:0.8em;text-transform:uppercase;letter-spacing:0.05em;margin-bottom:6px;">Filament by tool</div>';
            fuHTML += '<table style="width:100%;font-size:0.85em;border-collapse:collapse;">';
            fuHTML += '<tr style="color:#666;font-size:0.78em;"><th style="text-align:left;padding:3px 6px;">Tool</th><th style="text-align:left;padding:3px 6px;">Load</th><th style="text-align:left;padding:3px 6px;">Spool</th><th style="text-align:right;padding:3px 6px;">mm</th><th style="text-align:right;padding:3px 6px;">grams</th></tr>';
            r.filament_usages.forEach(function(fu) {
                fuHTML += '<tr style="border-top:1px solid #2a2a2a;">' +
                    '<td style="padding:4px 6px;">T' + fu.tool_index + '</td>' +
                    '<td style="padding:4px 6px;color:#888;">#' + fu.change_number + '</td>' +
                    '<td style="padding:4px 6px;color:#888;">' + (fu.spool_id > 0 ? '#' + fu.spool_id : '—') + '</td>' +
                    '<td style="padding:4px 6px;text-align:right;">' + fu.filament_used_mm.toFixed(0) + '</td>' +
                    '<td style="padding:4px 6px;text-align:right;color:#c8b8ff;">' + fu.filament_used_grams.toFixed(2) + ' g</td>' +
                    '</tr>';
            });
            fuHTML += '</table></div>';
            fuSection.innerHTML = fuHTML;
            fuSection.style.display = 'block';
        } else {
            fuSection.innerHTML = '';
            fuSection.style.display = 'none';
        }
    }

    // Pauses detail
    var pauseSection = document.getElementById('historyPauses');
    if (pauseSection) {
        if (r.pauses && r.pauses.length > 0) {
            var pHTML = '<div style="margin-top:12px;"><div style="color:#888;font-size:0.8em;text-transform:uppercase;letter-spacing:0.05em;margin-bottom:6px;">Pauses</div>';
            r.pauses.forEach(function(p) {
                pHTML += '<div style="padding:4px 0;border-top:1px solid #2a2a2a;font-size:0.85em;">' +
                    '<span style="color:#888;">' + _fmtDateFull(p.paused_at) + '</span>' +
                    ' · <span style="color:#aaa;">' + _fmtSec(p.duration_sec) + '</span>' +
                    (p.reason ? ' · <span style="color:#ffb347;">' + _esc(p.reason) + '</span>' : '') +
                    '</div>';
            });
            pHTML += '</div>';
            pauseSection.innerHTML = pHTML;
            pauseSection.style.display = 'block';
        } else {
            pauseSection.innerHTML = '';
            pauseSection.style.display = 'none';
        }
    }

    // Cost breakdown
    var costSection = document.getElementById('historyDetailCost');
    var recalcBtn   = document.getElementById('historyRecalcBtn');
    if (r.total_cost > 0) {
        costSection.style.display = 'block';
        document.getElementById('historyDetailCostRows').innerHTML =
            '<p style="color:#aaa;font-size:0.85em;margin:0;">Stored total: ' + _fmtCost(r.total_cost, r.currency) + '</p>';
        if (recalcBtn) recalcBtn.style.display = '';
    } else {
        costSection.style.display = 'none';
        if (recalcBtn) recalcBtn.style.display = r.filament_used > 0 ? '' : 'none';
    }

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
            // propagate into _allSessions
            _allSessions.forEach(function(s) {
                (s.records || []).forEach(function(r) {
                    if (r.id === _activeEntry.id) r.notes = note;
                });
            });
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
            // Remove the record from its session; remove the session if empty
            _allSessions = _allSessions.reduce(function(acc, s) {
                s.records = (s.records || []).filter(function(r) { return r.id !== _activeEntry.id; });
                if (s.records.length > 0) {
                    s.tool_count = s.records.length;
                    acc.push(s);
                }
                return acc;
            }, []);
            filterHistory();
            closeHistoryModal();
        })
        .catch(function(err) { alert('Error: ' + err.message); });
}

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
            if (typeof _renderCostRows === 'function') {
                document.getElementById('historyDetailCostRows').innerHTML = _renderCostRows(bd, bd.currency);
            } else {
                document.getElementById('historyDetailCostRows').innerHTML =
                    '<p>Total: ' + _fmtCost(bd.total_cost, bd.currency) + '</p>';
            }
            _activeEntry.total_cost = bd.total_cost;
            _activeEntry.currency   = bd.currency;
            _allSessions.forEach(function(s) {
                (s.records || []).forEach(function(r) {
                    if (r.id === _activeEntry.id) { r.total_cost = bd.total_cost; }
                });
                // Resum session total
                s.total_cost = (s.records || []).reduce(function(sum, r) { return sum + (r.total_cost || 0); }, 0);
            });
            renderTable();
        })
        .catch(function(err) { alert('Error: ' + err.message); });
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function _timeFromSession(s) {
    // Prefer explicit print_duration_sec when available (OctoPrint), fall back to print_time_minutes
    if (s.records && s.records.length > 0) {
        var r = s.records[0];
        if (r.print_duration_sec > 0) return _fmtSec(r.print_duration_sec);
        if (r.print_time_minutes > 0) return _fmtMin(r.print_time_minutes);
    }
    return '—';
}

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

function _fmtSec(sec) {
    if (!sec || sec <= 0) return '—';
    return _fmtMin(sec / 60);
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
    return '<span style="' + (styles[s] || styles.completed) +
           'padding:2px 8px;border-radius:10px;font-size:0.8em;white-space:nowrap;">' + s + '</span>';
}

function _sourceBadge(source) {
    var map = {
        octoprint: { bg: '#2a1a3d', color: '#b48aff', label: 'OctoPrint' },
        virtual:   { bg: '#1a3a2a', color: '#6ee7a0', label: 'Virtual'   },
        prusalink: { bg: '#1a2a3d', color: '#7ab8f5', label: 'PrusaLink' },
    };
    var cfg = map[source] || map.prusalink;
    return ' <span style="background:' + cfg.bg + ';color:' + cfg.color + ';padding:1px 6px;' +
           'border-radius:8px;font-size:0.72em;white-space:nowrap;margin-left:4px;">' + cfg.label + '</span>';
}

function _sourceLabel(source) {
    var labels = { octoprint: 'OctoPrint', virtual: 'Virtual printer', prusalink: 'PrusaLink' };
    return labels[source] || source || 'PrusaLink';
}

// ─── Init ─────────────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', function() {
    var tabs = document.querySelectorAll('.tab');
    tabs.forEach(function(btn) {
        btn.addEventListener('click', function() {
            if (btn.getAttribute('onclick') && btn.getAttribute('onclick').includes('history')) {
                if (_allSessions.length === 0) loadHistory();
            }
        });
    });

    window.addEventListener('click', function(e) {
        var m = document.getElementById('historyDetailModal');
        if (m && e.target === m) closeHistoryModal();
    });
});
