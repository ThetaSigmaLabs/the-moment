// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

// Dashboard stats and live-printer card rendering.

function _spoolChipTextColor(hex) {
    const r = parseInt(hex.slice(0, 2), 16), g = parseInt(hex.slice(2, 4), 16), b = parseInt(hex.slice(4, 6), 16);
    return (r * 299 + g * 587 + b * 114) / 1000 > 128 ? '#111' : '#fff';
}

async function loadDashboardStats() {
    try {
        const [statsResp, statusResp, histResp, spoolsResp] = await Promise.all([
            fetch('/api/stats'),
            fetch('/api/status'),
            fetch('/api/history?limit=5'),
            fetch('/api/spools'),
        ]);
        if (statsResp.ok) renderDashboardStats(await statsResp.json());
        if (statusResp.ok) { const d = await statusResp.json(); const spools = spoolsResp.ok ? await spoolsResp.json() : []; renderDashboardPrinters(d.printers, d.toolhead_mappings, spools); }
        if (histResp.ok) renderRecentPrints((await histResp.json()).records || []);
    } catch (e) {
        console.error('Dashboard load error:', e);
    }
}

function renderDashboardStats(s) {
    const set = (id, v) => { const el = document.getElementById(id); if (el) el.textContent = v; };

    set('stat-prints-30d', s.prints_30d ?? '—');

    const g = s.filament_used_30d_g || 0;
    set('stat-filament-30d', g === 0 ? '—' : g >= 1000 ? (g / 1000).toFixed(2) + ' kg' : Math.round(g) + ' g');

    const cost = s.total_cost_30d || 0;
    const sym = s.currency && s.currency.length === 3 ? s.currency + ' ' : '';
    set('stat-cost-30d', cost > 0 ? sym + cost.toFixed(2) : '—');

    const min = s.avg_print_time_min || 0;
    if (min <= 0) {
        set('stat-avg-time', '—');
    } else if (min >= 60) {
        set('stat-avg-time', Math.floor(min / 60) + 'h ' + Math.round(min % 60) + 'm');
    } else {
        set('stat-avg-time', Math.round(min) + 'm');
    }
}

function renderDashboardPrinters(printers, mappings, spools = []) {
    const spoolMap = {};
    spools.forEach(s => { spoolMap[s.id] = s; });

    const container = document.getElementById('dashboard-printers');
    if (!container) return;

    if (!printers || Object.keys(printers).length === 0) {
        container.innerHTML = '<p style="color:var(--text-secondary);font-size:0.9em;">No printers configured. <a href="#" onclick="switchTab(\'printers\');return false;" style="color:var(--brand-light);">Add a printer →</a></p>';
        return;
    }

    const sorted = Object.entries(printers).sort(([, a], [, b]) => {
        const diff = (a.sort_order || 0) - (b.sort_order || 0);
        return diff !== 0 ? diff : (a.name || '').localeCompare(b.name || '');
    });

    container.innerHTML = sorted.map(([id, p]) => {
        const state = (p.state || 'IDLE').toUpperCase();
        const label = state === 'VIRTUAL' ? 'READY' : state;
        const pm = (mappings && mappings[id]) || {};
        const spools = Object.entries(pm)
            .filter(([, m]) => m.spool_id)
            .map(([tid, m]) => {
                const s = spoolMap[m.spool_id];
                const colorHex = s?.filament?.color_hex;
                const chip = colorHex
                    ? `<span style="background:#${escapeHtml(colorHex)};color:${_spoolChipTextColor(colorHex)};padding:1px 6px;border-radius:3px;font-size:0.85em;font-weight:600;border:2px solid rgba(0,0,0,0.30);">#${m.spool_id}</span>`
                    : `#${m.spool_id}`;
                const textPart = s?.material ? escapeHtml(s.material) : '';
                const content = textPart ? `${chip} ${textPart}` : chip;
                return `<span class="dashboard-printer-spool">T${tid}: ${content}</span>`;
            })
            .join('');

        const jobLine = p.job_name
            ? `<div class="dashboard-printer-job">🖨️ ${escapeHtml(p.job_name)}</div>`
            : '';

        const debugBadge = p.debug_log
            ? `<span title="PrusaLink comms logging enabled" style="font-size:0.72em;background:rgba(255,160,0,0.18);color:#ffa000;border:1px solid rgba(255,160,0,0.35);border-radius:4px;padding:1px 5px;margin-left:6px;vertical-align:middle;">DEBUG</span>`
            : '';

        const isActive = ['PRINTING', 'PAUSED', 'ATTENTION'].includes(state);
        const viewPrintBtn = isActive
            ? `<button class="btn btn-small btn-secondary dashboard-view-print-btn" style="margin-top:8px;align-self:flex-start;" onclick="openActivePrintModal('${escapeHtml(id)}')">View Print →</button>`
            : '';

        const filamentWarnings = (p.filament_warnings || [])
            .map(w => `<div class="dashboard-printer-warning">⚠ ${escapeHtml(w.message)}</div>`)
            .join('');

        return `
            <div class="dashboard-printer-card" data-dashboard-printer-id="${escapeHtml(id)}">
                <div class="dashboard-printer-header">
                    <span class="dashboard-printer-name">${escapeHtml(p.name || id)}${debugBadge}</span>
                    <span class="status ${state.toLowerCase()}">${label}</span>
                </div>
                ${jobLine}
                <div class="dashboard-printer-progress-wrap">${buildProgressHTML(p)}</div>
                ${spools ? `<div class="dashboard-printer-spools">${spools}</div>` : ''}
                ${filamentWarnings ? `<div class="dashboard-printer-warnings">${filamentWarnings}</div>` : ''}
                <div class="dashboard-printer-snapshot-badge" style="display:none;margin-top:6px;"></div>
                ${viewPrintBtn}
                <button class="btn btn-small btn-secondary" style="margin-top:8px;align-self:flex-start;" onclick="switchToSpoolsForPrinter('${escapeHtml(id)}')">Assign Spool →</button>
            </div>`;
    }).join('');
}

function switchToSpoolsForPrinter(printerId) {
    switchTab('spools');
    setTimeout(() => {
        const el = document.querySelector(`.printer[data-printer-id="${printerId}"]`);
        if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }, 150);
}

function renderRecentPrints(records) {
    const container = document.getElementById('dashboard-recent-prints');
    if (!container) return;

    if (!records.length) {
        container.innerHTML = '<p style="color:var(--text-secondary);font-size:0.9em;">No prints yet.</p>';
        return;
    }

    container.innerHTML = records.map(r => {
        const icon = r.status === 'cancelled' ? '⚠️' : r.status === 'failed' ? '❌' : '✅';
        const name = escapeHtml(r.job_name || r.filename || 'Unknown');
        const printer = escapeHtml(r.printer_name || '');
        const grams = r.filament_used > 0 ? Math.round(r.filament_used) + 'g' : '—';
        const when = relativeTime(r.print_started || r.print_finished);

        return `<div class="dashboard-print-row" onclick="switchTab('history');setTimeout(()=>openHistoryModal(${r.id}),250)">
            <span class="dashboard-print-status">${icon}</span>
            <span class="dashboard-print-name" title="${name}">${name}</span>
            <span class="dashboard-print-printer">${printer}</span>
            <span class="dashboard-print-filament">${grams}</span>
            <span class="dashboard-print-date">${when}</span>
        </div>`;
    }).join('');
}

function formatDuration(seconds) {
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m`;
    return '<1m';
}

function buildProgressHTML(p) {
    const state = (p.state || '').toUpperCase();
    if (!['PRINTING', 'PAUSED', 'ATTENTION'].includes(state)) return '';

    const pct = Math.min(100, Math.max(0, p.progress || 0));
    const heating = (p.target_nozzle || 0) > 0 && (p.temp_nozzle || 0) < (p.target_nozzle || 0) - 10;
    const timeLeft = (p.time_remaining || 0) > 0 ? formatDuration(p.time_remaining) : '';

    let label;
    if (heating && pct < 2) {
        label = 'Heating up' + (timeLeft ? ` · ${timeLeft} left` : '');
    } else {
        label = `${pct.toFixed(1)}%` + (timeLeft ? ` · ${timeLeft} left` : '');
    }

    return `<div class="dashboard-printer-progress">
        <div class="dashboard-printer-progress-bar"><div class="dashboard-printer-progress-fill" style="width:${pct}%"></div></div>
        <span class="dashboard-printer-progress-label">${label}</span>
    </div>`;
}

function relativeTime(isoStr) {
    if (!isoStr) return '';
    const diff = Date.now() - new Date(isoStr).getTime();
    const min = Math.floor(diff / 60000);
    if (min < 60) return min + 'm ago';
    const hr = Math.floor(min / 60);
    if (hr < 24) return hr + 'h ago';
    return Math.floor(hr / 24) + 'd ago';
}

// Per-printer last-fetch timestamps for active-snapshot polling (rate-limit: 60s).
const _snapshotLastFetch = {};

function _fetchActiveSnapshots(printerId) {
    _snapshotLastFetch[printerId] = Date.now();
    fetch('/api/printers/' + printerId + '/active-snapshots')
        .then(r => r.json())
        .then(data => {
            const card = document.querySelector(`[data-dashboard-printer-id="${printerId}"]`);
            if (!card) return;
            const el = card.querySelector('.dashboard-printer-snapshot-badge');
            if (!el) return;
            const n = (data.snapshots || []).length;
            if (n > 0) {
                el.style.display = '';
                el.innerHTML = `<span style="font-size:0.8em;color:#a98eff;cursor:pointer;" onclick="switchTab('history')" title="Progress snapshots captured so far">📸 ${n} progress photo${n !== 1 ? 's' : ''}</span>`;
            } else {
                el.style.display = 'none';
            }
        })
        .catch(() => { });
}

// Called by websocket.js updateDashboard() to keep dashboard printer cards in sync.
function updateDashboardPrinterStatus(printerId, printerData) {
    const card = document.querySelector(`[data-dashboard-printer-id="${printerId}"]`);
    if (!card) return;
    const badge = card.querySelector('.status');
    if (!badge) return;
    const state = (printerData.state || 'IDLE').toUpperCase();
    badge.className = `status ${state.toLowerCase()}`;
    badge.textContent = state === 'VIRTUAL' ? 'READY' : state;

    const progressWrap = card.querySelector('.dashboard-printer-progress-wrap');
    if (progressWrap) progressWrap.innerHTML = buildProgressHTML(printerData);

    // Refresh active-snapshot badge for PRINTING printers (max once per 60s).
    const snapEl = card.querySelector('.dashboard-printer-snapshot-badge');
    if (state === 'PRINTING') {
        const last = _snapshotLastFetch[printerId] || 0;
        if (Date.now() - last > 60000) {
            _fetchActiveSnapshots(printerId);
        }
    } else if (snapEl) {
        snapEl.style.display = 'none';
        delete _snapshotLastFetch[printerId];
    }

    // Refresh filament sufficiency warnings.
    const warningsEl = card.querySelector('.dashboard-printer-warnings');
    const warnings = printerData.filament_warnings || [];
    if (warnings.length > 0) {
        const html = warnings.map(w => `<div class="dashboard-printer-warning">⚠ ${escapeHtml(w.message)}</div>`).join('');
        if (warningsEl) {
            warningsEl.innerHTML = html;
        } else {
            const newEl = document.createElement('div');
            newEl.className = 'dashboard-printer-warnings';
            newEl.innerHTML = html;
            const snapshotBadge = card.querySelector('.dashboard-printer-snapshot-badge');
            if (snapshotBadge) card.insertBefore(newEl, snapshotBadge);
        }
    } else if (warningsEl) {
        warningsEl.remove();
    }

    // Show/hide "View Print" button reactively as printer state changes.
    const isActive = ['PRINTING', 'PAUSED', 'ATTENTION'].includes(state);
    const viewBtn = card.querySelector('.dashboard-view-print-btn');
    if (isActive && !viewBtn) {
        const btn = document.createElement('button');
        btn.className = 'btn btn-small btn-secondary dashboard-view-print-btn';
        btn.style.cssText = 'margin-top:8px;align-self:flex-start;';
        btn.textContent = 'View Print →';
        btn.onclick = function () { openActivePrintModal(printerId); };
        const assignBtn = card.querySelector('button[onclick*="switchToSpoolsForPrinter"]');
        if (assignBtn) card.insertBefore(btn, assignBtn);
    } else if (!isActive && viewBtn) {
        viewBtn.remove();
        if (typeof _apmPrinterId !== 'undefined' && _apmPrinterId === printerId) {
            closeActivePrintModal();
        }
    }
}

// ─── Active Print Modal ───────────────────────────────────────────────────────

let _apmPrinterId = null;
let _apmRefreshTimer = null;
const _apmTabIds = ['live', 'snapshots', 'filament'];

function openActivePrintModal(printerId) {
    _apmPrinterId = printerId;
    document.getElementById('activePrintModal').style.display = 'block';
    switchActivePrintTab('live');
    _apmLoad();
    _apmRefreshTimer = setInterval(_apmLoad, 10000);
}

function closeActivePrintModal() {
    const m = document.getElementById('activePrintModal');
    if (m) m.style.display = 'none';
    _apmPrinterId = null;
    if (_apmRefreshTimer) { clearInterval(_apmRefreshTimer); _apmRefreshTimer = null; }
}

function switchActivePrintTab(tab) {
    document.querySelectorAll('#activePrintModal .hm-tab').forEach(function (btn) {
        btn.classList.toggle('active', btn.dataset.apmTab === tab);
    });
    _apmTabIds.forEach(function (t) {
        const el = document.getElementById('apmTab-' + t);
        if (el) el.style.display = (t === tab) ? 'block' : 'none';
    });
}

function _apmLoad() {
    if (!_apmPrinterId) return;
    fetch('/api/printers/' + _apmPrinterId + '/active-print')
        .then(function (r) {
            if (r.status === 409) {
                _apmSetRefreshStatus('Print ended.');
                if (_apmRefreshTimer) { clearInterval(_apmRefreshTimer); _apmRefreshTimer = null; }
                return null;
            }
            if (!r.ok) return null;
            return r.json();
        })
        .then(function (data) { if (data) _apmPopulate(data); })
        .catch(function () { });
}

function _apmPopulate(d) {
    const titleEl = document.getElementById('activePrintTitle');
    if (titleEl) titleEl.textContent = d.printer_name || d.printer_id;

    const stateEl = document.getElementById('activePrintState');
    if (stateEl) {
        const s = (d.state || '').toUpperCase();
        stateEl.textContent = s;
        stateEl.style.color = s === 'PRINTING' ? '#4caf50' : s === 'PAUSED' ? '#ffa000' : '#ef5350';
    }

    const pct = Math.min(100, Math.max(0, d.progress || 0));
    const pctEl = document.getElementById('apm-progress-pct');
    if (pctEl) pctEl.textContent = pct.toFixed(1) + '%';
    const barEl = document.getElementById('apm-progress-bar');
    if (barEl) barEl.style.width = pct + '%';

    const remEl = document.getElementById('apm-time-remaining');
    if (remEl) remEl.textContent = (d.time_remaining || 0) > 0 ? formatDuration(d.time_remaining) + ' remaining' : '';

    const elEl = document.getElementById('apm-time-printing');
    if (elEl) elEl.textContent = (d.time_printing || 0) > 0 ? formatDuration(d.time_printing) : '—';

    const zEl = document.getElementById('apm-axis-z');
    if (zEl) zEl.textContent = (d.axis_z || 0) > 0 ? d.axis_z.toFixed(2) : '—';

    const nozzEl = document.getElementById('apm-temp-nozzle');
    if (nozzEl) nozzEl.textContent = (d.temp_nozzle || 0) > 0
        ? d.temp_nozzle.toFixed(1) + ' / ' + (d.target_nozzle || 0).toFixed(0) + '°C'
        : '—';

    const bedEl = document.getElementById('apm-temp-bed');
    if (bedEl) bedEl.textContent = (d.temp_bed || 0) > 0
        ? d.temp_bed.toFixed(1) + ' / ' + (d.target_bed || 0).toFixed(0) + '°C'
        : '—';

    const fsEl = document.getElementById('apm-flow-speed');
    if (fsEl) fsEl.textContent = (d.flow || d.speed)
        ? (d.flow || 0) + '% / ' + (d.speed || 0) + '%'
        : '—';

    const fanEl = document.getElementById('apm-fans');
    if (fanEl) fanEl.textContent = (d.fan_hotend || d.fan_print)
        ? (d.fan_hotend || 0) + '% / ' + (d.fan_print || 0) + '%'
        : '—';

    const jobEl = document.getElementById('apm-job-name');
    if (jobEl) jobEl.textContent = d.job_name || '—';

    // Snapshots tab
    const snaps = d.snapshots || [];
    const snapBtn = document.getElementById('apmTab-snapshots-btn');
    if (snapBtn) snapBtn.style.display = snaps.length > 0 ? '' : 'none';
    const snapList = document.getElementById('apm-snapshot-list');
    if (snapList) {
        if (snaps.length === 0) {
            snapList.innerHTML = '<span style="color:#555;">No snapshots yet.</span>';
        } else {
            _snapshotList = snaps.map(function (s) {
                return { url: s.url, label: s.label || s.filename || '' };
            });
            snapList.innerHTML = '<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(90px,1fr));gap:10px;">' +
                snaps.map(function (s, idx) {
                    const label = escapeHtml(s.label || s.filename);
                    const url = escapeHtml(s.url);
                    return '<div style="text-align:center;">' +
                        '<img src="' + url + '" alt="' + label + '" ' +
                        'style="width:90px;height:90px;object-fit:cover;border-radius:4px;cursor:zoom-in;display:block;" ' +
                        'onclick="openSnapshotLightbox(' + idx + ')">' +
                        '<div style="font-size:0.72em;color:#777;margin-top:4px;word-break:break-all;">' + label + '</div>' +
                        '</div>';
                }).join('') +
                '</div>';
        }
    }

    // Filament tab
    const toolheads = d.toolheads || [];
    const thEl = document.getElementById('apm-toolheads');
    if (thEl) {
        if (toolheads.length === 0) {
            thEl.innerHTML = '<span style="color:#555;">No toolhead data.</span>';
        } else {
            thEl.innerHTML = toolheads.map(function (t) {
                const dot = t.color_hex
                    ? '<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#' +
                    escapeHtml(t.color_hex) + ';margin-right:6px;vertical-align:middle;flex-shrink:0;"></span>'
                    : '';
                const spoolInfo = t.spool_id > 0
                    ? dot + escapeHtml(t.material || '') + (t.brand ? ' · ' + escapeHtml(t.brand) : '') +
                    ' <span style="color:#666;font-size:0.88em;">#' + t.spool_id + '</span>'
                    : '<span style="color:#555;">No spool assigned</span>';
                return '<div style="display:flex;align-items:center;padding:8px 0;border-bottom:1px solid #222;">' +
                    '<span style="color:#888;min-width:90px;font-size:0.88em;flex-shrink:0;">' + escapeHtml(t.display_name) + '</span>' +
                    '<span style="display:flex;align-items:center;">' + spoolInfo + '</span>' +
                    '</div>';
            }).join('');
        }
    }

    _apmSetRefreshStatus('Updated ' + new Date().toLocaleTimeString());
}

function _apmSetRefreshStatus(msg) {
    const el = document.getElementById('apm-refresh-status');
    if (el) el.textContent = msg;
}

// Close on backdrop click
document.addEventListener('click', function (e) {
    const m = document.getElementById('activePrintModal');
    if (m && e.target === m) closeActivePrintModal();
});
