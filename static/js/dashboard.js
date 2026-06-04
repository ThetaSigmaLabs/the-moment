// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

// Dashboard stats and live-printer card rendering.

async function loadDashboardStats() {
    try {
        const [statsResp, statusResp, histResp] = await Promise.all([
            fetch('/api/stats'),
            fetch('/api/status'),
            fetch('/api/history?limit=5'),
        ]);
        if (statsResp.ok)  renderDashboardStats(await statsResp.json());
        if (statusResp.ok) { const d = await statusResp.json(); renderDashboardPrinters(d.printers, d.toolhead_mappings); }
        if (histResp.ok)   renderRecentPrints((await histResp.json()).records || []);
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

function renderDashboardPrinters(printers, mappings) {
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
        const state  = (p.state || 'IDLE').toUpperCase();
        const label  = state === 'VIRTUAL' ? 'READY' : state;
        const pm     = (mappings && mappings[id]) || {};
        const spools = Object.entries(pm)
            .filter(([, m]) => m.spool_id)
            .map(([tid, m]) => `<span class="dashboard-printer-spool">T${tid}: ${escapeHtml(m.material || ('Spool #' + m.spool_id))}</span>`)
            .join('');

        const jobLine = p.job_name
            ? `<div class="dashboard-printer-job">🖨️ ${escapeHtml(p.job_name)}</div>`
            : '';

        const debugBadge = p.debug_log
            ? `<span title="PrusaLink comms logging enabled" style="font-size:0.72em;background:rgba(255,160,0,0.18);color:#ffa000;border:1px solid rgba(255,160,0,0.35);border-radius:4px;padding:1px 5px;margin-left:6px;vertical-align:middle;">DEBUG</span>`
            : '';

        return `
            <div class="dashboard-printer-card" data-dashboard-printer-id="${escapeHtml(id)}">
                <div class="dashboard-printer-header">
                    <span class="dashboard-printer-name">${escapeHtml(p.name || id)}${debugBadge}</span>
                    <span class="status ${state.toLowerCase()}">${label}</span>
                </div>
                ${jobLine}
                ${spools ? `<div class="dashboard-printer-spools">${spools}</div>` : ''}
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
        const icon    = r.status === 'cancelled' ? '⚠️' : r.status === 'failed' ? '❌' : '✅';
        const name    = escapeHtml(r.job_name || r.filename || 'Unknown');
        const printer = escapeHtml(r.printer_name || '');
        const grams   = r.filament_used > 0 ? Math.round(r.filament_used) + 'g' : '—';
        const when    = relativeTime(r.print_started || r.print_finished);

        return `<div class="dashboard-print-row" onclick="switchTab('history');setTimeout(()=>openHistoryModal(${r.id}),250)">
            <span class="dashboard-print-status">${icon}</span>
            <span class="dashboard-print-name" title="${name}">${name}</span>
            <span class="dashboard-print-printer">${printer}</span>
            <span class="dashboard-print-filament">${grams}</span>
            <span class="dashboard-print-date">${when}</span>
        </div>`;
    }).join('');
}

function relativeTime(isoStr) {
    if (!isoStr) return '';
    const diff = Date.now() - new Date(isoStr).getTime();
    const min  = Math.floor(diff / 60000);
    if (min < 60)  return min + 'm ago';
    const hr = Math.floor(min / 60);
    if (hr  < 24)  return hr + 'h ago';
    return Math.floor(hr / 24) + 'd ago';
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
}
