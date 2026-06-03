// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — Print History Tab

// ─── State ────────────────────────────────────────────────────────────────────

var _allSessions      = [];   // raw PrintSession objects from /api/sessions
var _filteredSessions = [];
var _sortField        = 'print_finished';
var _sortAsc          = false;
var _activeEntry      = null;
var _spoolMap         = {};   // id → SpoolmanSpool, populated on modal open
var _expandedSessions = {};   // session key → true when expanded
var _selectedKeys     = {};   // session key → true when selected
var _currentPage      = 1;
var _perPage          = parseInt(localStorage.getItem('history_per_page') || '25', 10);

// ─── Load & Render ────────────────────────────────────────────────────────────

function loadHistory() {
    fetch('/api/sessions?limit=500')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            _allSessions = data.sessions || [];
            _selectedKeys = {};
            filterHistory();
        })
        .catch(function(err) {
            document.getElementById('historyBody').innerHTML =
                '<tr><td colspan="11" style="text-align:center;padding:30px;color:#ef9a9a;">' +
                'Failed to load history: ' + err.message + '</td></tr>';
        });
}

function filterHistory() {
    _currentPage = 1;
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
    if (!skipToggle) _currentPage = 1;
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
    var tbody = document.getElementById('historyBody');
    var empty = document.getElementById('historyEmpty');
    var count = document.getElementById('historyCount');
    var total = _filteredSessions.length;

    var totalPages = (_perPage === 0 || total === 0) ? 1 : Math.ceil(total / _perPage);
    if (_currentPage > totalPages) _currentPage = totalPages;
    if (_currentPage < 1) _currentPage = 1;

    var start = _perPage === 0 ? 0 : (_currentPage - 1) * _perPage;
    var end   = _perPage === 0 ? total : Math.min(start + _perPage, total);

    if (count) {
        if (total === 0) {
            count.textContent = '0 sessions';
        } else if (_perPage === 0 || totalPages <= 1) {
            count.textContent = total + ' session' + (total !== 1 ? 's' : '');
        } else {
            count.textContent = (start + 1) + '–' + end + ' of ' + total + ' sessions';
        }
    }

    _renderPagination(totalPages, _currentPage);

    if (total === 0) {
        tbody.innerHTML = '';
        if (empty) empty.style.display = 'block';
        return;
    }
    if (empty) empty.style.display = 'none';

    var html = '';
    _filteredSessions.slice(start, end).forEach(function(s, i) {
        var globalI  = start + i;
        var key      = _sessionKey(s, globalI);
        var multi    = s.tool_count > 1;
        var expanded = !!_expandedSessions[key];
        html += buildSessionRow(s, globalI, key, multi, expanded);
        if (multi && expanded) {
            (s.records || []).forEach(function(r) { html += buildSubRow(r); });
        }
    });
    tbody.innerHTML = html;
    _syncSelection();
}

function _renderPagination(totalPages, currentPage) {
    var show = totalPages > 1;
    ['historyPagTop', 'historyPagBottom'].forEach(function(id) {
        var el = document.getElementById(id);
        if (!el) return;
        if (!show) { el.innerHTML = ''; return; }
        var btn = 'padding:5px 14px;border-radius:6px;border:1px solid #555;background:#2a2a2a;color:#ccc;cursor:pointer;font-size:0.85em;';
        var off = 'opacity:0.35;cursor:default;';
        var atFirst = currentPage <= 1;
        var atLast  = currentPage >= totalPages;
        el.innerHTML =
            '<div style="display:flex;align-items:center;justify-content:center;gap:10px;padding:8px 20px;">' +
            '<button style="' + btn + (atFirst ? off : '') + '"' + (atFirst ? ' disabled' : '') +
            ' onclick="changePage(-1)">&#8592; Prev</button>' +
            '<span style="color:#888;font-size:0.85em;">Page ' + currentPage + ' of ' + totalPages + '</span>' +
            '<button style="' + btn + (atLast ? off : '') + '"' + (atLast ? ' disabled' : '') +
            ' onclick="changePage(1)">Next &#8594;</button>' +
            '</div>';
    });
}

function changePage(delta) {
    _currentPage += delta;
    renderTable();
}

function setPerPage(val) {
    _perPage = parseInt(val, 10);
    if (isNaN(_perPage)) _perPage = 25;
    localStorage.setItem('history_per_page', String(_perPage));
    _currentPage = 1;
    renderTable();
}

// ─── Row builders ─────────────────────────────────────────────────────────────

function buildSessionRow(s, i, key, multi, expanded) {
    var date   = _fmtDate(s.print_finished);
    var usage  = s.total_filament_grams > 0 ? s.total_filament_grams.toFixed(1) + ' g' : '—';
    var time   = _timeFromSession(s);
    var cost   = s.total_cost > 0 ? _fmtCost(s.total_cost, s.currency) : '—';
    var firstRec = s.records && s.records[0] ? s.records[0] : {};
    var isRecovered = !!firstRec.recovered;
    var hasPending  = !!firstRec.has_pending_download;
    var statusBadge = isRecovered
        ? '<span style="background:#3d2800;color:#ffa040;padding:2px 8px;border-radius:10px;font-size:0.8em;white-space:nowrap;" title="Print was in-progress when the service restarted. Filament data is incomplete.">incomplete</span>'
        : _statusBadge(s.status);
    var sourceBadge = _sourceBadge(s.source);

    // Quality tags: use first record's tags
    var tags = (s.records && s.records[0]) ? (s.records[0].tags || []) : [];
    var qualityCell = _renderTagBadges(tags);

    // Thumbnail: use first record's thumbnail if available
    var thumbSrc = '';
    if (s.records && s.records.length > 0) {
        for (var ri = 0; ri < s.records.length; ri++) {
            if (s.records[ri].thumbnail_base64) { thumbSrc = s.records[ri].thumbnail_base64; break; }
        }
    }
    var thumbCell = thumbSrc
        ? '<img src="' + _esc(thumbSrc) + '" style="width:40px;height:40px;object-fit:cover;border-radius:4px;border:1px solid rgba(102,126,234,0.45);box-shadow:0 0 6px rgba(102,126,234,0.25);background:#e0e0e0;display:block;margin:auto;">'
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
    var pendingBadge = '';
    if (hasPending) {
        var dlId = firstRec.pending_download_id;
        pendingBadge = ' <span style="background:#2a1800;color:#ffa040;padding:1px 6px;border-radius:8px;font-size:0.72em;white-space:nowrap;" title="G-code file download is pending. Click Retry to attempt again.">pending download</span>' +
            ' <button onclick="event.stopPropagation();retryDownload(' + dlId + ', this)" ' +
            'style="background:#3d2a00;color:#ffa040;border:1px solid #664400;border-radius:4px;padding:0 6px;font-size:0.72em;cursor:pointer;white-space:nowrap;" title="Retry download now">↻ Retry</button>';
    }
    var firstRecID = (s.records && s.records[0]) ? s.records[0].id : 0;
    var renameBtn = firstRecID
        ? ' <button onclick="event.stopPropagation();_renameFromTable(' + firstRecID + ',this)" ' +
          'title="Rename" style="background:none;border:none;color:#555;cursor:pointer;font-size:0.78em;padding:0 3px;vertical-align:middle;">✏</button>'
        : '';
    var fileCell = expandIcon + _esc(file) + toolBadge + renameBtn + pendingBadge;

    // Note: aggregate — show first record's note if any
    var note = '';
    if (s.records && s.records[0] && s.records[0].notes) {
        var n = s.records[0].notes;
        note = _esc(n.substring(0, 40)) + (n.length > 40 ? '…' : '');
    }

    var onclick = multi
        ? 'toggleSession(\'' + _esc(key) + '\')'
        : (s.records && s.records[0] ? 'openHistoryModal(' + s.records[0].id + ')' : '');

    var rowStyle = 'border-bottom:1px solid #2a2a2a;cursor:pointer;transition:background 0.15s;' +
        (multi ? 'border-left:3px solid #1a3a5c;' : '');
    var chkChecked = _selectedKeys[key] ? ' checked' : '';

    return '<tr onclick="' + onclick + '" ' +
        'style="' + rowStyle + '" ' +
        'onmouseover="this.style.background=\'rgba(255,255,255,0.04)\'" ' +
        'onmouseout="this.style.background=\'\'">' +
        '<td onclick="event.stopPropagation();" style="padding:9px 8px;width:32px;text-align:center;">' +
        '<input type="checkbox"' + chkChecked + ' style="cursor:pointer;width:15px;height:15px;" ' +
        'onchange="toggleSessionSelect(\'' + _esc(key) + '\', this)"></td>' +
        '<td style="padding:9px 12px;white-space:nowrap;color:#aaa;">' + date + '</td>' +
        '<td style="padding:9px 12px;text-align:center;">' + thumbCell + '</td>' +
        '<td style="padding:9px 12px;white-space:nowrap;">' + _esc(s.printer_name) + sourceBadge + '</td>' +
        '<td style="padding:9px 12px;max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="' + _esc(s.job_name) + '">' + fileCell + '</td>' +
        '<td style="padding:9px 12px;text-align:right;white-space:nowrap;">' + usage + '</td>' +
        '<td style="padding:9px 12px;text-align:right;white-space:nowrap;color:#aaa;">' + time + '</td>' +
        '<td style="padding:9px 12px;text-align:right;white-space:nowrap;' + (s.total_cost > 0 ? 'color:#c8b8ff;' : 'color:#555;') + '">' + cost + '</td>' +
        '<td style="padding:9px 12px;color:#888;font-size:0.85em;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">' + note + '</td>' +
        '<td style="padding:9px 12px;text-align:center;">' + statusBadge + '</td>' +
        '<td style="padding:9px 12px;text-align:center;">' + qualityCell + '</td>' +
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
        '<td style="padding:6px 8px;width:32px;"></td>' +
        '<td style="padding:6px 12px;color:#666;font-size:0.82em;"></td>' +
        '<td style="padding:6px 12px;color:#777;font-size:0.82em;padding-left:28px;">T' + r.toolhead_id + ' · spool&nbsp;' + (r.spool_id > 0 ? '#' + r.spool_id : '—') + '</td>' +
        '<td style="padding:6px 12px;color:#666;font-size:0.82em;padding-left:28px;">' + _esc(_shortName(r.job_name)) + '</td>' +
        '<td style="padding:6px 12px;text-align:right;color:#aaa;font-size:0.82em;">' + usage + '</td>' +
        '<td style="padding:6px 12px;text-align:right;color:#666;font-size:0.82em;">' + time + '</td>' +
        '<td style="padding:6px 12px;text-align:right;color:#666;font-size:0.82em;">' + cost + '</td>' +
        '<td style="padding:6px 12px;font-size:0.82em;color:#555;">' + _esc((r.notes || '').substring(0, 40)) + '</td>' +
        '<td style="padding:6px 12px;text-align:center;">' + _statusBadge(r.status) + '</td>' +
        '<td style="padding:6px 12px;text-align:center;">' + _renderTagBadges(r.tags || []) + '</td>' +
        '<td style="padding:6px 12px;text-align:center;">' +
        (r.thumbnail_base64 ? '<img src="' + _esc(r.thumbnail_base64) + '" style="width:30px;height:30px;object-fit:cover;border-radius:3px;border:1px solid rgba(102,126,234,0.35);box-shadow:0 0 4px rgba(102,126,234,0.18);background:#e0e0e0;display:block;margin:auto;">' : '') +
        '</td>' +
        '</tr>';
}

function toggleSession(key) {
    _expandedSessions[key] = !_expandedSessions[key];
    renderTable();
}

// ─── History Detail Modal ─────────────────────────────────────────────────────

var _activeModalTab = 'details';

function switchModalTab(tab) {
    _activeModalTab = tab;
    document.querySelectorAll('.hm-tab').forEach(function(btn) {
        btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    ['details','costs','quality','filament','files','snapshots','debuglog'].forEach(function(t) {
        var el = document.getElementById('hmTab-' + t);
        if (el) el.style.display = (t === tab) ? 'block' : 'none';
    });
    if (tab === 'debuglog' && _activeEntry) {
        _loadDebugLog(_activeEntry.id);
    }
    if (tab === 'snapshots' && _activeEntry) {
        _loadSnapshots(_activeEntry.id);
    }
}

function _loadDebugLog(id) {
    var ta = document.getElementById('hmDebugLogText');
    if (!ta || ta.dataset.loadedFor === String(id)) return;
    ta.value = 'Loading…';
    fetch('/api/history/' + id + '/debug-log')
        .then(function(r) { return r.text(); })
        .then(function(text) {
            ta.value = text || '(no log entries)';
            ta.dataset.loadedFor = String(id);
        })
        .catch(function(err) {
            ta.value = 'Error loading log: ' + err.message;
        });
}

function _loadSnapshots(printID) {
    var listEl = document.getElementById('historySnapshotList');
    if (!listEl || listEl.dataset.loadedFor === String(printID)) return;
    listEl.dataset.loadedFor = String(printID);

    fetch('/api/history/' + printID + '/attachments')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var snaps = (data.attachments || []).filter(function(a) { return a.file_type === 'camera'; });
            var snapBtn = document.getElementById('hmTab-snapshots-btn');
            if (snapBtn) snapBtn.style.display = snaps.length > 0 ? '' : 'none';
            if (snaps.length === 0) {
                listEl.innerHTML = '<span style="color:#555;font-size:0.875em;">No snapshots for this print</span>';
                return;
            }
            listEl.innerHTML = '<table style="width:100%;border-collapse:collapse;">' +
                snaps.map(function(a) {
                    var snapUrl = '/api/history/attachments/' + a.id + '/download';
                    var ts = a.stored_at ? a.stored_at.replace('T', ' ').replace('Z', '').substring(0, 19) : '';
                    var size = a.file_size > 1048576
                        ? (a.file_size / 1048576).toFixed(1) + ' MB'
                        : a.file_size > 1024
                            ? (a.file_size / 1024).toFixed(0) + ' KB'
                            : a.file_size + ' B';
                    return '<tr style="border-bottom:1px solid #2a2a2a;">' +
                        '<td style="padding:8px 8px 8px 0;width:72px;vertical-align:middle;">' +
                            '<img src="' + snapUrl + '" alt="snapshot" ' +
                                'style="width:64px;height:64px;object-fit:cover;border-radius:4px;cursor:zoom-in;display:block;" ' +
                                'onclick="openSnapshotLightbox(\'' + snapUrl + '\')">' +
                        '</td>' +
                        '<td style="padding:8px;vertical-align:middle;">' +
                            '<div style="color:#d0d0d0;font-size:0.88em;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="' + _esc(a.filename) + '">' + _esc(a.filename) + '</div>' +
                            '<div style="color:#555;font-size:0.78em;margin-top:2px;">' + ts + (ts ? ' &nbsp;·&nbsp; ' : '') + size + '</div>' +
                        '</td>' +
                        '<td style="padding:8px;vertical-align:middle;white-space:nowrap;text-align:right;">' +
                            '<a href="' + snapUrl + '" download="' + _esc(a.filename) + '" ' +
                                'class="btn btn-small btn-secondary" style="padding:2px 8px;font-size:0.78em;text-decoration:none;margin-right:4px;">↓</a>' +
                            '<button class="btn btn-small btn-danger" style="padding:2px 8px;font-size:0.78em;" ' +
                                'onclick="deleteHistoryAttachment(' + a.id + ',' + printID + ')">✕</button>' +
                        '</td>' +
                        '</tr>';
                }).join('') +
                '</table>';
        })
        .catch(function() {
            listEl.innerHTML = '<span style="color:#ef9a9a;font-size:0.9em;">Failed to load snapshots</span>';
        });
}

function openSnapshotLightbox(url) {
    var lb = document.getElementById('snapshotLightbox');
    var img = document.getElementById('snapshotLightboxImg');
    if (!lb || !img) return;
    img.src = url;
    lb.style.display = 'flex';
}

function closeSnapshotLightbox() {
    var lb = document.getElementById('snapshotLightbox');
    if (lb) lb.style.display = 'none';
}

function copyDebugLog() {
    var ta = document.getElementById('hmDebugLogText');
    if (!ta) return;
    ta.select();
    try {
        document.execCommand('copy');
    } catch(e) {
        if (navigator.clipboard) navigator.clipboard.writeText(ta.value);
    }
}

function _formatSpoolLabel(spoolId) {
    if (!spoolId || spoolId <= 0) return '—';
    var s = _spoolMap[spoolId];
    if (!s) return '[' + spoolId + ']';
    var label = '[' + spoolId + '] ' + (s.material || 'Unknown Material') + ' - ' + (s.brand || 'Unknown Brand') + ' - ' + (s.name || 'Unnamed Spool');
    if (s.remaining_weight != null) label += ' (' + Math.round(s.remaining_weight) + 'g remaining)';
    return label;
}

function openHistoryModal(id) {
    Promise.all([
        fetch('/api/history/' + id).then(function(r) { return r.json(); }),
        fetch('/api/spools').then(function(r) { return r.json(); })
    ])
    .then(function(results) {
        var record = results[0];
        var spoolsData = results[1];
        _spoolMap = {};
        var spools = spoolsData.spools || spoolsData || [];
        spools.forEach(function(s) { _spoolMap[s.id] = s; });
        _activeEntry = record;
        populateModal(record);
        document.getElementById('historyDetailModal').style.display = 'block';
    })
    .catch(function(err) {
        showToast('Failed to load record: ' + err.message);
    });
}

function populateModal(r) {
    // Show/hide Debug Log tab and reset its content
    var dlBtn = document.getElementById('hmTab-debuglog-btn');
    if (dlBtn) dlBtn.style.display = r.has_debug_log ? '' : 'none';
    var dlTa = document.getElementById('hmDebugLogText');
    if (dlTa) { dlTa.value = 'Loading…'; dlTa.dataset.loadedFor = ''; }

    // Reset snapshots tab — shown after load if any camera attachments exist
    var snapBtn = document.getElementById('hmTab-snapshots-btn');
    if (snapBtn) snapBtn.style.display = 'none';
    var snapList = document.getElementById('historySnapshotList');
    if (snapList) { snapList.innerHTML = 'Loading…'; snapList.dataset.loadedFor = ''; }

    switchModalTab('details');
    var titleEl = document.getElementById('historyDetailTitle');
    titleEl.dataset.jobName = r.job_name || '';
    _renderModalTitle(titleEl, r.id, r.job_name);

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

    // PrusaLink virtual segment — toolhead_id > 0 means this was split from an attention event.
    if (r.source === 'prusalink' && r.toolhead_id > 0) {
        rows.push(['Note', '<span style="color:#ffc107;font-size:0.85em;">Attention-event segment. If a different spool was loaded, reassign it below.</span>']);
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
        return '<tr><td style="padding:5px 14px 5px 0;color:#777;white-space:nowrap;vertical-align:top;font-size:0.9em;">' + row[0] +
            '</td><td style="padding:5px 0;word-break:break-all;color:#d0d0d0;">' + row[1] + '</td></tr>';
    }).join('');

    // Filament usages (OctoPrint multi-spool / multi-tool detail)
    var fuSection = document.getElementById('historyFilamentUsages');
    if (fuSection) {
        if (r.filament_usages && r.filament_usages.length > 0) {
            // Only show the "Swap #" column when a mid-print filament change actually happened.
            // change_number=0 is the initial load (always present); >0 means a swap occurred.
            var hasSwaps = r.filament_usages.some(function(fu) { return fu.change_number > 0; });
            var fuHTML = '<div style="font-size:0.75em;color:#777;text-transform:uppercase;letter-spacing:0.06em;margin-bottom:10px;">Filament by Tool</div>';
            fuHTML += '<table style="width:100%;font-size:0.875em;border-collapse:collapse;">';
            fuHTML += '<tr style="color:#888;font-size:0.78em;border-bottom:1px solid #333;">' +
                '<th style="text-align:left;padding:5px 8px;font-weight:500;">Tool</th>' +
                (hasSwaps ? '<th style="text-align:left;padding:5px 8px;font-weight:500;" title="0 = initial load; 1, 2… = mid-print filament swaps">Swap #</th>' : '') +
                '<th style="text-align:left;padding:5px 8px;font-weight:500;">Spool</th>' +
                '<th style="text-align:right;padding:5px 8px;font-weight:500;">mm</th>' +
                '<th style="text-align:right;padding:5px 8px;font-weight:500;">grams</th>' +
                '<th style="text-align:right;padding:5px 8px;font-weight:500;">Cost/kg</th>' +
                '<th style="text-align:right;padding:5px 8px;font-weight:500;">Est. cost</th>' +
                '<th style="padding:5px 8px;"></th>' +
                '</tr>';
            r.filament_usages.forEach(function(fu) {
                var spoolLabel = _formatSpoolLabel(fu.spool_id);
                var priceCell, estCostCell;
                if (fu.price_per_kg != null && fu.price_per_kg > 0) {
                    var estCost = (fu.filament_used_grams / 1000) * fu.price_per_kg;
                    priceCell  = '<td style="padding:6px 8px;text-align:right;color:#aaa;">' + fu.price_per_kg.toFixed(2) + '</td>';
                    estCostCell = '<td style="padding:6px 8px;text-align:right;color:#c8b8ff;">' + estCost.toFixed(3) + '</td>';
                } else {
                    priceCell   = '<td style="padding:6px 8px;text-align:right;color:#555;">—</td>';
                    estCostCell = '<td style="padding:6px 8px;text-align:right;color:#555;">—</td>';
                }
                fuHTML += '<tr style="border-top:1px solid #2a2a2a;" id="fu-row-' + fu.id + '">' +
                    '<td style="padding:6px 8px;color:#ccc;">T' + fu.tool_index + '</td>' +
                    (hasSwaps ? '<td style="padding:6px 8px;color:#777;">' + (fu.change_number === 0 ? '—' : '#' + fu.change_number) + '</td>' : '') +
                    '<td style="padding:6px 8px;color:#aaa;" id="fu-spool-' + fu.id + '">' + _esc(spoolLabel) + '</td>' +
                    '<td style="padding:6px 8px;text-align:right;color:#aaa;">' + fu.filament_used_mm.toFixed(0) + '</td>' +
                    '<td style="padding:6px 8px;text-align:right;color:#c8b8ff;" id="fu-grams-' + fu.id + '">' + fu.filament_used_grams.toFixed(2) + ' g</td>' +
                    priceCell +
                    estCostCell +
                    '<td style="padding:6px 8px;">' +
                        '<button class="btn btn-small btn-secondary" style="padding:2px 7px;font-size:0.78em;" ' +
                        'onclick="openReassignPicker(' + fu.id + ',' + r.id + ',' + fu.filament_used_grams + ')">↔ Reassign</button>' +
                    '</td>' +
                    '</tr>';
            });
            fuHTML += '</table>';
            fuSection.innerHTML = fuHTML;
        } else {
            fuSection.innerHTML = '<p style="color:#555;font-size:0.875em;padding:24px 0;text-align:center;margin:0;">No per-tool filament data recorded for this print.</p>';
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

    // Cost breakdown (Costs tab)
    var costSection = document.getElementById('historyDetailCost');
    var costEmpty   = document.getElementById('hmCostEmpty');
    var recalcBtn   = document.getElementById('historyRecalcBtn');
    var hasFilament = r.filament_used > 0 || (r.filament_usages && r.filament_usages.length > 0);
    if (r.total_cost > 0) {
        if (costSection) costSection.style.display = 'block';
        if (costEmpty)   costEmpty.style.display   = 'none';
        document.getElementById('historyDetailCostRows').innerHTML =
            '<p style="color:#aaa;font-size:0.85em;margin:0 0 4px;">Stored total: <strong style="color:#c8b8ff;">' + _fmtCost(r.total_cost, r.currency) + '</strong></p>' +
            '<p style="color:#555;font-size:0.8em;margin:0;">Click Recalculate to recompute from current rates.</p>';
        if (recalcBtn) recalcBtn.style.display = '';
    } else {
        if (costSection) costSection.style.display = 'none';
        if (costEmpty)   costEmpty.style.display   = 'block';
        if (recalcBtn) recalcBtn.style.display = hasFilament ? '' : 'none';
    }

    document.getElementById('historyNoteInput').value = r.notes || '';

    // Populate tag editor
    _populateTagEditor(r.tags || []);

    // Load file attachments and check for snapshots (to show/hide Snapshots tab)
    _loadAttachments(r.id);
    _loadSnapshots(r.id);
}

function _loadAttachments(printID) {
    var listEl = document.getElementById('historyAttachmentList');
    if (!listEl) return;
    fetch('/api/history/' + printID + '/attachments')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var items = (data.attachments || []).filter(function(a) { return a.file_type !== 'camera'; });
            if (items.length === 0) {
                listEl.innerHTML = '<span style="color:#555;font-size:0.875em;">No files attached</span>';
                return;
            }
            listEl.innerHTML = items.map(function(a) {
                var typeColor = a.file_type === 'gcode' ? '#7ab8f5' : a.file_type === 'slicer' ? '#b48aff' : '#aaa';
                var typeBadge = '<span style="background:#1a2a3a;color:' + typeColor + ';padding:1px 6px;border-radius:8px;font-size:0.78em;margin-right:6px;">' + _esc(a.file_type) + '</span>';
                var size = a.file_size > 1048576
                    ? (a.file_size / 1048576).toFixed(1) + ' MB'
                    : a.file_size > 1024
                        ? (a.file_size / 1024).toFixed(0) + ' KB'
                        : a.file_size + ' B';
                var renameBtn = a.file_type === 'gcode'
                    ? '<button id="att-rename-' + a.id + '" title="Rename" ' +
                      'style="background:none;border:none;color:#555;cursor:pointer;font-size:0.85em;padding:0 3px;vertical-align:middle;" ' +
                      'onclick="_renameAttachmentFromList(' + a.id + ',' + printID + ')">✏</button>'
                    : '';
                return '<div style="display:flex;align-items:center;justify-content:space-between;padding:5px 0;border-top:1px solid #2a2a2a;">' +
                    '<div style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1;min-width:0;">' +
                        typeBadge +
                        '<span id="att-name-' + a.id + '" title="' + _esc(a.filename) + '">' + _esc(a.filename) + '</span>' +
                        renameBtn +
                        '<span style="color:#555;margin-left:8px;font-size:0.82em;">' + size + '</span>' +
                    '</div>' +
                    '<div style="display:flex;gap:6px;flex-shrink:0;margin-left:8px;">' +
                        '<a href="/api/history/attachments/' + a.id + '/download" download="' + _esc(a.filename) + '" ' +
                            'class="btn btn-small btn-secondary" style="padding:2px 8px;font-size:0.78em;text-decoration:none;">↓ Download</a>' +
                        '<button class="btn btn-small btn-danger" style="padding:2px 8px;font-size:0.78em;" ' +
                            'onclick="deleteHistoryAttachment(' + a.id + ', ' + printID + ')">✕</button>' +
                    '</div>' +
                    '</div>';
            }).join('');
        })
        .catch(function() {
            listEl.innerHTML = '<span style="color:#ef9a9a;font-size:0.9em;">Failed to load attachments</span>';
        });
}

function deleteHistoryAttachment(attachID, printID) {
    if (!confirm('Delete this attachment? The file cannot be recovered.')) return;
    fetch('/api/history/attachments/' + attachID, { method: 'DELETE' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { showToast('Error: ' + data.error); return; }
            _loadAttachments(printID);
        })
        .catch(function(e) { showToast('Request failed: ' + e); });
}

function uploadHistoryAttachment() {
    if (!_activeEntry) return;
    var input = document.getElementById('historyAttachInput');
    if (!input || !input.files || input.files.length === 0) return;
    var file = input.files[0];
    var formData = new FormData();
    formData.append('file', file);
    fetch('/api/history/' + _activeEntry.id + '/attachments', { method: 'POST', body: formData })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { showToast('Upload failed: ' + data.error); return; }
            input.value = '';
            _loadAttachments(_activeEntry.id);
        })
        .catch(function(e) { showToast('Upload failed: ' + e); });
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
            if (data.error) { showToast('Error: ' + data.error); return; }
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
        .catch(function(err) { showToast('Error: ' + err.message); });
}

function deleteHistoryEntry() {
    if (!_activeEntry) return;
    if (!confirm('Delete this print history record?\nThis cannot be undone.')) return;
    fetch('/api/history/' + _activeEntry.id, { method: 'DELETE' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { showToast('Error: ' + data.error); return; }
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
        .catch(function(err) { showToast('Error: ' + err.message); });
}

function recalcHistoryCost() {
    if (!_activeEntry) return;
    var body;
    if (_activeEntry.filament_usages && _activeEntry.filament_usages.length > 0) {
        body = {
            filament:       _activeEntry.filament_usages.map(function(fu) {
                return {
                    tool_index:          fu.tool_index,
                    change_number:       fu.change_number,
                    spool_id:            fu.spool_id,
                    filament_used_mm:    fu.filament_used_mm,
                    filament_used_grams: fu.filament_used_grams
                };
            }),
            print_time_min: _activeEntry.print_time_minutes,
            printer_name:   _activeEntry.printer_name
        };
    } else {
        body = {
            filament_grams: _activeEntry.filament_used,
            print_time_min: _activeEntry.print_time_minutes,
            spool_id:       _activeEntry.spool_id,
            printer_name:   _activeEntry.printer_name
        };
    }
    fetch('/api/cost/calculate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
    })
        .then(function(r) { return r.json(); })
        .then(function(bd) {
            if (bd.error) { showToast('Error: ' + bd.error); return; }
            var costSection = document.getElementById('historyDetailCost');
            var costEmpty   = document.getElementById('hmCostEmpty');
            if (costSection) costSection.style.display = 'block';
            if (costEmpty)   costEmpty.style.display   = 'none';
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
        .catch(function(err) { showToast('Error: ' + err.message); });
}

// ─── Bulk Selection & Delete ──────────────────────────────────────────────────

function _sessionKey(s, i) {
    if (s.session_id) return s.session_id;
    return '__solo_' + (s.records && s.records[0] ? s.records[0].id : i);
}

function toggleSessionSelect(key, checkbox) {
    if (checkbox.checked) {
        _selectedKeys[key] = true;
    } else {
        delete _selectedKeys[key];
    }
    _syncSelection();
}

function toggleSelectAll(checked) {
    _selectedKeys = {};
    if (checked) {
        _filteredSessions.forEach(function(s, i) {
            _selectedKeys[_sessionKey(s, i)] = true;
        });
    }
    renderTable();
}

function _syncSelection() {
    var total = _filteredSessions.length;
    var selectedCount = 0;
    _filteredSessions.forEach(function(s, i) {
        if (_selectedKeys[_sessionKey(s, i)]) selectedCount++;
    });

    var allCb = document.getElementById('historySelectAll');
    if (allCb) {
        allCb.checked = total > 0 && selectedCount === total;
        allCb.indeterminate = selectedCount > 0 && selectedCount < total;
    }

    var recalcSelBtn = document.getElementById('historyRecalcSelectedBtn');
    if (recalcSelBtn) {
        if (selectedCount > 0) {
            recalcSelBtn.style.display = '';
            recalcSelBtn.textContent = '💰 Recalc (' + selectedCount + ')';
        } else {
            recalcSelBtn.style.display = 'none';
        }
    }

    var btn = document.getElementById('historyDeleteSelectedBtn');
    if (btn) {
        if (selectedCount > 0) {
            btn.style.display = '';
            btn.textContent = '🗑 Delete (' + selectedCount + ')';
        } else {
            btn.style.display = 'none';
        }
    }
}

function recalcSelectedSessions() {
    var ids = [];
    var sessionCount = 0;
    _filteredSessions.forEach(function(s, i) {
        if (!_selectedKeys[_sessionKey(s, i)]) return;
        sessionCount++;
        (s.records || []).forEach(function(r) { ids.push(r.id); });
    });
    if (ids.length === 0) return;

    fetch('/api/history/batch-recalc', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ids: ids })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (data.error) { showToast('Error: ' + data.error); return; }
        var costMap = {};
        (data.results || []).forEach(function(r) {
            if (!r.error) costMap[r.id] = r.total_cost;
        });
        _allSessions.forEach(function(s) {
            (s.records || []).forEach(function(r) {
                if (r.id in costMap) r.total_cost = costMap[r.id];
            });
            s.total_cost = (s.records || []).reduce(function(sum, r) { return sum + (r.total_cost || 0); }, 0);
        });
        _selectedKeys = {};
        filterHistory();
        var errCount = (data.results || []).filter(function(r) { return r.error; }).length;
        if (errCount > 0) {
            showToast('Updated ' + data.updated + ' records. ' + errCount + ' failed.');
        }
    })
    .catch(function(err) { showToast('Error: ' + err.message); });
}

function deleteSelectedSessions() {
    var ids = [];
    var sessionCount = 0;
    _filteredSessions.forEach(function(s, i) {
        if (!_selectedKeys[_sessionKey(s, i)]) return;
        sessionCount++;
        (s.records || []).forEach(function(r) { ids.push(r.id); });
    });
    if (ids.length === 0) return;

    var msg = 'Delete ' + sessionCount + ' session' + (sessionCount !== 1 ? 's' : '') +
        ' (' + ids.length + ' record' + (ids.length !== 1 ? 's' : '') + ')?\nThis cannot be undone.';
    if (!confirm(msg)) return;

    fetch('/api/history/batch', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ids: ids })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (data.error) { showToast('Error: ' + data.error); return; }
        var deleted = {};
        ids.forEach(function(id) { deleted[id] = true; });
        _allSessions = _allSessions.reduce(function(acc, s) {
            s.records = (s.records || []).filter(function(r) { return !deleted[r.id]; });
            if (s.records.length > 0) {
                s.tool_count = s.records.length;
                acc.push(s);
            }
            return acc;
        }, []);
        _selectedKeys = {};
        filterHistory();
    })
    .catch(function(err) { showToast('Error: ' + err.message); });
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

// ─── Filament segment reassignment ───────────────────────────────────────────

var _reassignSegmentID = 0;
var _reassignPrintID   = 0;

function openReassignPicker(segmentID, printID, gramsUsed) {
    _reassignSegmentID = segmentID;
    _reassignPrintID   = printID;

    var picker     = document.getElementById('reassignPicker');
    var sel        = document.getElementById('reassignSpoolSelect');
    var gramsInput = document.getElementById('reassignGrams');
    if (!picker || !sel) return;

    if (gramsInput) gramsInput.value = (gramsUsed || 0).toFixed(2);

    sel.innerHTML = '<option value="">Loading…</option>';
    picker.style.display = 'block';

    fetch('/api/spools')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var spools = data.spools || data || [];
            spools.forEach(function(s) { _spoolMap[s.id] = s; });
            sel.innerHTML = '<option value="0">— no spool (clear) —</option>';
            spools.forEach(function(s) {
                var opt = document.createElement('option');
                opt.value = s.id;
                opt.textContent = _formatSpoolLabel(s.id);
                sel.appendChild(opt);
            });
        })
        .catch(function() {
            sel.innerHTML = '<option value="">Failed to load spools</option>';
        });
}

function confirmReassign() {
    var sel        = document.getElementById('reassignSpoolSelect');
    var gramsInput = document.getElementById('reassignGrams');
    var newID      = parseInt(sel ? sel.value : '0', 10) || 0;
    var newGrams   = parseFloat(gramsInput ? gramsInput.value : '0') || 0;
    var picker     = document.getElementById('reassignPicker');

    fetch('/api/prints/' + _reassignPrintID + '/filament/' + _reassignSegmentID + '/reassign', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ spool_id: newID, grams: newGrams })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (data.error) { showToast('Reassign failed: ' + data.error); return; }
        // Update spool and grams cells in the row without reloading.
        var spoolCell = document.getElementById('fu-spool-' + _reassignSegmentID);
        if (spoolCell) spoolCell.textContent = _formatSpoolLabel(newID);
        var gramsCell = document.getElementById('fu-grams-' + _reassignSegmentID);
        if (gramsCell && newGrams > 0) gramsCell.textContent = newGrams.toFixed(2) + ' g';
        if (picker) picker.style.display = 'none';
        // Reload cost row to reflect updated price.
        if (_activeEntry) recalcHistoryCost();
    })
    .catch(function(e) { showToast('Request failed: ' + e); });
}

function cancelReassign() {
    var picker = document.getElementById('reassignPicker');
    if (picker) picker.style.display = 'none';
}

// ─── Quality Tags ─────────────────────────────────────────────────────────────

var _OUTCOME_TAGS = ['success', 'acceptable', 'failed'];
var _ISSUE_LABELS = {
    'bed-adhesion':      'Bed Adhesion',
    'warping':           'Warping',
    'stringing':         'Stringing',
    'spaghetti':         'Spaghetti',
    'layer-shift':       'Layer Shift',
    'layer-delamination':'Layer Delam.',
    'under-extrusion':   'Under-Extrusion',
    'over-extrusion':    'Over-Extrusion',
    'seam-blobbing':     'Seam Blobbing',
    'vfa':               'VFA',
    'ghosting':          'Ghosting',
    'elephant-foot':     'Elephant Foot',
    'filament-jam':      'Filament Jam',
    'nozzle-clog':       'Nozzle Clog',
    'thermal-issue':     'Thermal Issue',
    'power-failure':     'Power Failure',
    'user-cancelled':    'Cancelled',
    'custom':            null   // rendered with custom_text
};

var _OUTCOME_ICONS = { success: '✅', acceptable: '👍', failed: '❌' };

function _renderTagBadges(tags) {
    if (!tags || tags.length === 0) return '<span style="color:#444;font-size:0.8em;">—</span>';
    var html = '';
    var outcome = tags.find(function(t) { return _OUTCOME_TAGS.indexOf(t.tag) !== -1; });
    if (outcome) {
        html += '<span class="tag-outcome-badge ' + _esc(outcome.tag) + '">' +
            (_OUTCOME_ICONS[outcome.tag] || '') + ' ' + _esc(outcome.tag) + '</span> ';
    }
    var issues = tags.filter(function(t) { return _OUTCOME_TAGS.indexOf(t.tag) === -1; });
    if (issues.length > 0) {
        var first = issues[0];
        var label = first.tag === 'custom' ? _esc(first.custom_text || 'Custom') : (_ISSUE_LABELS[first.tag] || _esc(first.tag));
        html += '<span class="tag-issue-badge">' + label + '</span>';
        if (issues.length > 1) {
            html += '<span class="tag-issue-badge">+' + (issues.length - 1) + '</span>';
        }
    }
    return html || '<span style="color:#444;font-size:0.8em;">—</span>';
}

var _currentOutcome = '';

function _populateTagEditor(tags) {
    _currentOutcome = '';
    // Reset outcome buttons
    _OUTCOME_TAGS.forEach(function(o) {
        var btn = document.getElementById('tagOutcome' + o.charAt(0).toUpperCase() + o.slice(1));
        if (btn) { btn.className = 'tag-outcome-btn'; btn.style.background = 'transparent'; btn.style.color = '#aaa'; btn.style.borderColor = '#444'; }
    });
    // Reset checkboxes
    document.querySelectorAll('input[name="tag-issue"]').forEach(function(cb) { cb.checked = false; });
    var customInput = document.getElementById('tagCustomText');
    if (customInput) { customInput.value = ''; customInput.style.display = 'none'; }

    tags.forEach(function(t) {
        if (_OUTCOME_TAGS.indexOf(t.tag) !== -1) {
            _currentOutcome = t.tag;
            _applyOutcomeStyle(t.tag);
        } else {
            var cb = document.querySelector('input[name="tag-issue"][value="' + t.tag + '"]');
            if (cb) {
                cb.checked = true;
                if (t.tag === 'custom' && customInput) {
                    customInput.value = t.custom_text || '';
                    customInput.style.display = 'block';
                }
            }
        }
    });
}

function _applyOutcomeStyle(outcome) {
    _OUTCOME_TAGS.forEach(function(o) {
        var btn = document.getElementById('tagOutcome' + o.charAt(0).toUpperCase() + o.slice(1));
        if (!btn) return;
        if (o === outcome) {
            btn.className = 'tag-outcome-btn active-' + o;
        } else {
            btn.className = 'tag-outcome-btn';
            btn.style.background = 'transparent';
            btn.style.color = '#aaa';
            btn.style.borderColor = '#444';
        }
    });
}

function toggleOutcome(outcome) {
    _currentOutcome = (_currentOutcome === outcome) ? '' : outcome;
    if (_currentOutcome) {
        _applyOutcomeStyle(_currentOutcome);
    } else {
        _OUTCOME_TAGS.forEach(function(o) {
            var btn = document.getElementById('tagOutcome' + o.charAt(0).toUpperCase() + o.slice(1));
            if (btn) { btn.className = 'tag-outcome-btn'; btn.style.background = 'transparent'; btn.style.color = '#aaa'; btn.style.borderColor = '#444'; }
        });
    }
}

function toggleCustomText() {
    var cb = document.getElementById('tagIssueCustomCheck');
    var input = document.getElementById('tagCustomText');
    if (!cb || !input) return;
    input.style.display = cb.checked ? 'block' : 'none';
    if (cb.checked) input.focus();
}

function saveHistoryTags() {
    if (!_activeEntry) return;
    var issues = [];
    document.querySelectorAll('input[name="tag-issue"]:checked').forEach(function(cb) {
        issues.push(cb.value);
    });
    var customText = (document.getElementById('tagCustomText') || {}).value || '';
    var payload = { outcome: _currentOutcome, issues: issues, custom_text: customText };

    fetch('/api/history/' + _activeEntry.id + '/tags', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (data.error) { showToast('Error: ' + data.error); return; }
        var newTags = data.tags || [];
        _activeEntry.tags = newTags;
        // Propagate into _allSessions so the table updates without reload
        _allSessions.forEach(function(s) {
            (s.records || []).forEach(function(r) {
                if (r.id === _activeEntry.id) r.tags = newTags;
            });
        });
        renderTable();
    })
    .catch(function(err) { showToast('Error saving tags: ' + err.message); });
}

// ─── Inline Rename ────────────────────────────────────────────────────────────

// _startInlineEdit shows a text input for editing alongside el (el is hidden, not cleared).
// onSave(newValue, el, oldValue) is called when the user confirms a change.
// onClose() is called regardless of outcome (save, cancel, or no-change) — useful for cleanup.
function _startInlineEdit(el, currentValue, onSave, onClose) {
    if (el._editingActive) return;
    el._editingActive = true;

    var input = document.createElement('input');
    input.type = 'text';
    input.value = currentValue;
    input.style.cssText = 'background:#1a1a1a;border:1px solid #555;border-radius:4px;color:#e0e0e0;' +
        'font-size:inherit;font-family:inherit;padding:2px 6px;width:200px;box-sizing:border-box;vertical-align:middle;';

    // Insert input as a sibling after el; hide el so the span width doesn't constrain input.
    var savedDisplay = el.style.display;
    el.style.display = 'none';
    el.parentNode.insertBefore(input, el.nextSibling);
    input.focus();
    input.select();

    var done = false;
    function restore() {
        el.style.display = savedDisplay;
        el._editingActive = false;
        if (input.parentNode) input.parentNode.removeChild(input);
        if (onClose) onClose();
    }
    function commit() {
        if (done) return;
        done = true;
        var v = input.value.trim();
        restore();
        if (v && v !== currentValue) {
            onSave(v, el, currentValue);
        }
    }
    function cancel() {
        if (done) return;
        done = true;
        restore();
    }
    input.addEventListener('blur', commit);
    input.addEventListener('keydown', function(e) {
        if (e.key === 'Enter')  { e.preventDefault(); commit(); }
        if (e.key === 'Escape') { e.preventDefault(); cancel(); }
    });
}

// _applyRenameToSessions propagates a job_name change into _allSessions so the table
// stays in sync without a full reload.
function _applyRenameToSessions(printID, newName) {
    _allSessions.forEach(function(s) {
        if (s.records && s.records[0] && s.records[0].id === printID) {
            s.job_name = newName;
        }
        (s.records || []).forEach(function(r) {
            if (r.id === printID) r.job_name = newName;
        });
    });
}

// renamePrint is the public entry point called from table rows and the modal title.
// onClose is forwarded to _startInlineEdit for post-edit cleanup (e.g. renderTable).
function renamePrint(printID, el, currentValue, onClose) {
    _startInlineEdit(el, currentValue, function(newName, el2, oldVal) {
        el2.textContent = '…';
        fetch('/api/history/' + printID + '/name', {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: newName })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) {
                showToast(data.error);
                el2.textContent = oldVal;
                return;
            }
            el2.textContent = newName;
            _applyRenameToSessions(printID, newName);
            if (_activeEntry && _activeEntry.id === printID) {
                _activeEntry.job_name = newName;
            }
            // Refresh Files tab so gcode filename reflects the new name.
            _loadAttachments(printID);
            renderTable();
        })
        .catch(function(err) {
            showToast('Rename failed: ' + err.message);
            el2.textContent = oldVal;
        });
    }, onClose);
}

// renameAttachmentInline is called from the attachment filename span in the Files tab.
function renameAttachmentInline(attachmentID, printID, el, currentFilename) {
    _startInlineEdit(el, currentFilename, function(newFilename, el2, oldVal) {
        el2.textContent = '…';
        fetch('/api/history/attachments/' + attachmentID + '/rename', {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ filename: newFilename })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) {
                showToast(data.error);
                el2.textContent = oldVal;
                return;
            }
            el2.textContent = newFilename;
            // Derive the new job_name (strip extension) and sync everywhere
            var newJobName = newFilename.replace(/\.[^.]+$/, '');
            _applyRenameToSessions(printID, newJobName);
            var titleEl = document.getElementById('historyDetailTitle');
            if (titleEl && _activeEntry && _activeEntry.id === printID) {
                _activeEntry.job_name = newJobName;
                titleEl.dataset.jobName = newJobName;
                _renderModalTitle(titleEl, printID, newJobName);
            }
            renderTable();
        })
        .catch(function(err) {
            showToast('Rename failed: ' + err.message);
            el2.textContent = oldVal;
        });
    });
}

// _renderModalTitle sets the modal header title with an inline pencil icon.
function _renderModalTitle(el, printID, jobName) {
    el.innerHTML = '';
    var span = document.createElement('span');
    span.className = 'rename-job-name';
    span.textContent = jobName || 'Print Detail';
    el.appendChild(span);
    var btn = document.createElement('button');
    btn.title = 'Rename';
    btn.innerHTML = '✏';
    btn.style.cssText = 'background:none;border:none;color:#666;cursor:pointer;font-size:0.85em;' +
        'padding:0 0 0 7px;vertical-align:middle;line-height:1;';
    btn.addEventListener('click', function(e) {
        e.stopPropagation();
        renamePrint(printID, span, span.textContent);
    });
    el.appendChild(btn);
}

// _renameAttachmentFromList is called by the ✏ button in the Files tab attachment list.
function _renameAttachmentFromList(attachmentID, printID) {
    var nameEl = document.getElementById('att-name-' + attachmentID);
    if (!nameEl) return;
    var currentFilename = nameEl.textContent.trim();
    renameAttachmentInline(attachmentID, printID, nameEl, currentFilename);
}

// _renameFromTable is called by the ✏ button in a history table row.
function _renameFromTable(printID, btn) {
    var currentText = '';
    _allSessions.forEach(function(s) {
        (s.records || []).forEach(function(r) {
            if (r.id === printID) currentText = _shortName(r.job_name);
        });
        if (!currentText && s.records && s.records[0] && s.records[0].id === printID) {
            currentText = _shortName(s.job_name);
        }
    });
    if (!currentText) return;
    // Insert a temporary span before btn as the inline-edit anchor.
    // Pass renderTable as onClose so the row is always rebuilt on finish (cancel or save).
    var span = document.createElement('span');
    span.textContent = currentText;
    btn.parentNode.insertBefore(span, btn);
    renamePrint(printID, span, currentText, function() { renderTable(); });
}

// ─── Init ─────────────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', function() {
    var perPageSel = document.getElementById('historyPerPage');
    if (perPageSel) perPageSel.value = String(_perPage);
    loadHistory();

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

// Immediately retry a pending G-code download by its queue ID.
// The button is replaced with status text during/after the attempt.
function retryDownload(id, btn) {
    if (!btn) return;
    btn.disabled = true;
    btn.textContent = '…';
    fetch('/api/pending-downloads/' + id + '/retry', { method: 'POST' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                btn.textContent = '✓';
                btn.style.color = '#6ee7a0';
                // Reload the history table to reflect the new record.
                setTimeout(loadHistory, 800);
            } else {
                btn.textContent = '✗';
                btn.style.color = '#ff7070';
                btn.title = data.message || 'Retry failed';
                setTimeout(function() {
                    btn.disabled = false;
                    btn.textContent = '↻ Retry';
                    btn.style.color = '#ffa040';
                }, 3000);
            }
        })
        .catch(function() {
            btn.disabled = false;
            btn.textContent = '↻ Retry';
        });
}
