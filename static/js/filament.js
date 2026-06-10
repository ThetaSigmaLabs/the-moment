// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — Filament Calibration Tab

const CAL_FIELDS = [
    { key: 'diameter',               label: '⌀',            step: '0.01', min: '0' },
    { key: 'settings_bed_temp',      label: 'Bed',           step: '1',    min: '0', isInt: true },
    { key: 'settings_extruder_temp', label: 'Nozzle',        step: '1',    min: '0', isInt: true },
    { key: 'cal_max_flow_rate',      label: 'Max Flow',      step: '0.1',  min: '0' },
    { key: 'cal_pressure_advance',   label: 'PA',            step: '0.001',min: '0' },
    { key: 'cal_flow_ratio',         label: 'Flow Ratio',    step: '0.001',min: '0' },
    { key: 'cal_retraction_length',  label: 'Ret. Length',   step: '0.1',  min: '0' },
    { key: 'cal_retraction_speed',   label: 'Ret. Speed',    step: '1',    min: '0' },
];

let _allFilaments = [];

function loadFilaments() {
    fetch('/api/filaments')
        .then(r => r.json())
        .then(data => {
            _allFilaments = data;
            renderFilamentTable(data);
        })
        .catch(err => {
            document.getElementById('filament-tbody').innerHTML =
                `<tr><td colspan="11" style="text-align:center;color:var(--error-color);padding:24px;">
                    Failed to load filaments: ${err.message}
                </td></tr>`;
        });
}

function getCalValue(f, key) {
    if (key === 'diameter') return f.diameter ?? 0;
    if (key === 'settings_bed_temp') return f.settings_bed_temp ?? 0;
    if (key === 'settings_extruder_temp') return f.settings_extruder_temp ?? 0;
    const extra = f.extra || {};
    const v = extra[key];
    if (v === undefined || v === null) return 0;
    const n = parseFloat(v);
    return isNaN(n) ? 0 : n;
}

function renderFilamentTable(filaments) {
    const tbody = document.getElementById('filament-tbody');

    if (!filaments || filaments.length === 0) {
        tbody.innerHTML = `<tr><td colspan="11" style="text-align:center;color:var(--text-muted);padding:24px;">
            No filaments found in Spoolman.
        </td></tr>`;
        return;
    }

    // Group by vendor + material
    const groups = new Map();
    for (const f of filaments) {
        const vendor = f.vendor?.name ?? 'Unknown';
        const material = f.material ?? 'Unknown';
        const key = `${vendor}\x00${material}`;
        if (!groups.has(key)) groups.set(key, { vendor, material, filaments: [] });
        groups.get(key).filaments.push(f);
    }

    // Sort groups by vendor then material
    const sortedGroups = [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));

    const rows = [];
    for (const [groupKey, group] of sortedGroups) {
        const storageKey = `filament-group-${groupKey}`;
        const expanded = sessionStorage.getItem(storageKey) !== 'false';
        const count = group.filaments.length;
        const displayKey = groupKey.replace('\x00', ' — ');

        rows.push(`
<tr class="filament-group-row" data-group="${escAttr(groupKey)}"
    onclick="toggleFilamentGroup('${escAttr(groupKey)}')"
    style="cursor:pointer; background:var(--surface-2); user-select:none;">
    <td colspan="11" style="padding:8px 12px; font-weight:600; font-size:0.88em;
        color:var(--text-secondary); letter-spacing:0.03em;">
        <span class="filament-group-chevron" id="chevron-${escAttr(groupKey)}"
              style="display:inline-block; margin-right:6px; transition:transform 0.15s;
                     transform:${expanded ? 'rotate(90deg)' : 'rotate(0deg)'};">▶</span>
        ${escHtml(group.vendor)} &mdash; ${escHtml(group.material)}
        <span style="font-weight:400; color:var(--text-muted); margin-left:8px;">(${count})</span>
    </td>
</tr>`);

        for (const f of group.filaments) {
            const color = f.color_hex ? `#${f.color_hex}` : '#888';
            const inputs = CAL_FIELDS.map(field => {
                const val = getCalValue(f, field.key);
                const displayVal = field.isInt ? Math.round(val) : val;
                return `<td style="padding:4px 6px;">
    <input type="number" class="cal-input"
           data-filament-id="${f.id}"
           data-field="${escAttr(field.key)}"
           data-original="${displayVal}"
           value="${displayVal}"
           step="${field.step}" min="${field.min}"
           style="width:100%; min-width:52px; background:transparent; border:none;
                  border-bottom:1px solid transparent; color:var(--text-primary);
                  font-size:0.88em; padding:3px 2px; text-align:right;
                  border-radius:0; outline:none;"
           onfocus="this.style.borderBottomColor='var(--accent-color)'"
           onblur="handleCalBlur(this)">
</td>`;
            }).join('');

            rows.push(`
<tr class="filament-data-row" data-group="${escAttr(groupKey)}"
    data-filament-name="${escAttr((f.name ?? '').toLowerCase())}"
    data-filament-material="${escAttr((f.material ?? '').toLowerCase())}"
    data-filament-vendor="${escAttr((f.vendor?.name ?? '').toLowerCase())}"
    style="${expanded ? '' : 'display:none;'}">
    <td style="padding:4px 8px;">
        <div style="width:18px; height:18px; border-radius:50%;
                    background:${color}; border:1px solid rgba(255,255,255,0.15);
                    flex-shrink:0;"></div>
    </td>
    <td style="padding:4px 8px; white-space:nowrap; font-size:0.9em;">${escHtml(f.name ?? '')}</td>
    ${inputs}
    <td style="padding:4px 8px; text-align:right; white-space:nowrap;">
        <button class="btn btn-small btn-secondary"
                onclick="openEditFilamentModal(${f.id})"
                title="Edit this filament"
                style="font-size:0.75em; padding:3px 8px; margin-right:4px;">✎</button>
        <button class="btn btn-small btn-secondary"
                onclick="cloneFilament(${f.id})"
                title="Clone this filament"
                style="font-size:0.75em; padding:3px 8px;">⧉</button>
    </td>
</tr>`);
        }
    }

    tbody.innerHTML = rows.join('');
}

function expandAllFilaments() {
    document.querySelectorAll('.filament-data-row').forEach(r => { r.style.display = ''; });
    document.querySelectorAll('.filament-group-row').forEach(r => {
        const gk = r.dataset.group;
        sessionStorage.setItem(`filament-group-${gk}`, 'true');
        const chevron = document.getElementById(`chevron-${gk}`);
        if (chevron) chevron.style.transform = 'rotate(90deg)';
    });
}

function collapseAllFilaments() {
    document.querySelectorAll('.filament-data-row').forEach(r => { r.style.display = 'none'; });
    document.querySelectorAll('.filament-group-row').forEach(r => {
        const gk = r.dataset.group;
        sessionStorage.setItem(`filament-group-${gk}`, 'false');
        const chevron = document.getElementById(`chevron-${gk}`);
        if (chevron) chevron.style.transform = 'rotate(0deg)';
    });
}

function toggleFilamentGroup(groupKey) {
    const storageKey = `filament-group-${groupKey}`;
    const rows = document.querySelectorAll(`.filament-data-row[data-group="${CSS.escape(groupKey)}"]`);
    const chevron = document.getElementById(`chevron-${groupKey}`);
    const currentlyExpanded = sessionStorage.getItem(storageKey) !== 'false';
    const nowExpanded = !currentlyExpanded;
    sessionStorage.setItem(storageKey, nowExpanded ? 'true' : 'false');
    rows.forEach(r => { r.style.display = nowExpanded ? '' : 'none'; });
    if (chevron) chevron.style.transform = nowExpanded ? 'rotate(90deg)' : 'rotate(0deg)';
}

function handleCalBlur(input) {
    input.style.borderBottomColor = 'transparent';
    const newVal = parseFloat(input.value);
    const origVal = parseFloat(input.dataset.original);
    const filamentID = input.dataset.filamentId;
    const field = input.dataset.field;
    console.log('[cal-blur]', field, 'id:', filamentID, 'orig:', origVal, 'new:', newVal, 'skip:', isNaN(newVal) || newVal === origVal);
    if (isNaN(newVal) || newVal === origVal) return;

    fetch(`/api/filaments/${filamentID}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ field, value: newVal }),
    })
    .then(r => {
        console.log('[cal-blur]', field, 'PATCH status:', r.status);
        if (!r.ok) return r.json().then(j => Promise.reject(j.error || r.statusText));
        input.dataset.original = newVal;
        showToast('Saved', 'success');
    })
    .catch(err => {
        console.error('[cal-blur]', field, 'error:', err);
        input.value = origVal;
        showToast(`Save failed: ${err}`, 'error');
    });
}

function filterFilamentTable(query) {
    const q = query.toLowerCase().trim();
    if (!q) {
        document.querySelectorAll('.filament-group-row, .filament-data-row').forEach(r => {
            r.style.display = '';
        });
        // Restore collapsed state
        document.querySelectorAll('.filament-data-row').forEach(r => {
            const gk = r.dataset.group;
            const sk = `filament-group-${gk}`;
            if (sessionStorage.getItem(sk) === 'false') r.style.display = 'none';
        });
        return;
    }

    const matchedGroups = new Set();
    document.querySelectorAll('.filament-data-row').forEach(r => {
        const name = r.dataset.filamentName ?? '';
        const material = r.dataset.filamentMaterial ?? '';
        const vendor = r.dataset.filamentVendor ?? '';
        const match = name.includes(q) || material.includes(q) || vendor.includes(q);
        r.style.display = match ? '' : 'none';
        if (match) matchedGroups.add(r.dataset.group);
    });
    document.querySelectorAll('.filament-group-row').forEach(r => {
        r.style.display = matchedGroups.has(r.dataset.group) ? '' : 'none';
    });
}

// ── Edit Modal ───────────────────────────────────────────────────────────────

let _editFilamentID = null;
let _vendorCache = null;

async function openEditFilamentModal(id) {
    _editFilamentID = id;
    const f = _allFilaments.find(x => x.id === id);
    if (!f) return;

    // Populate vendor dropdown (cached after first load)
    if (!_vendorCache) {
        try {
            const r = await fetch('/api/vendors');
            _vendorCache = r.ok ? await r.json() : [];
        } catch (_) { _vendorCache = []; }
    }
    const sel = document.getElementById('filEdit-vendor');
    sel.innerHTML = '<option value="">— no vendor —</option>' +
        _vendorCache.map(v => `<option value="${v.id}">${escHtml(v.name)}</option>`).join('');
    sel.value = f.vendor ? String(f.vendor.id) : '';

    // Tab 1 — Details
    _setFE('filEdit-name',          f.name ?? '');
    _setFE('filEdit-material',      f.material ?? '');
    _setFE('filEdit-color',         f.color_hex ? '#' + f.color_hex : '#888888');
    _setFE('filEdit-multi-color',   f.multi_color_hexes ?? '');
    _setFE('filEdit-diameter',      f.diameter ?? 0);
    _setFE('filEdit-weight',        f.weight ?? 0);
    _setFE('filEdit-spool-weight',  f.spool_weight ?? 0);
    _setFE('filEdit-price',         f.price ?? '');
    _setFE('filEdit-density',       f.density ?? 0);
    _setFE('filEdit-extruder-temp', f.settings_extruder_temp ?? 0);
    _setFE('filEdit-bed-temp',      f.settings_bed_temp ?? 0);

    // Tab 2 — OpenPrintTag
    const ex = f.extra || {};
    _setFE('filEdit-mat-class',       _extraStr(ex, 'nfc_material_class') || 'FFF');
    _setFE('filEdit-country',         _extraStr(ex, 'nfc_country_of_origin'));
    _setFE('filEdit-min-print-temp',  _extraInt(ex, 'nfc_min_print_temp'));
    _setFE('filEdit-max-print-temp',  _extraInt(ex, 'nfc_max_print_temp'));
    _setFE('filEdit-min-bed-temp',    _extraInt(ex, 'nfc_min_bed_temp'));
    _setFE('filEdit-max-bed-temp',    _extraInt(ex, 'nfc_max_bed_temp'));
    _setFE('filEdit-nominal-length',  _extraInt(ex, 'nfc_nominal_length'));
    _setFE('filEdit-transmission',    _extraFloat(ex, 'nfc_transmission_distance'));
    _setFE('filEdit-mat-props',       _extraStr(ex, 'nfc_material_properties'));

    switchFilamentEditTab('details');
    document.getElementById('editFilamentModal').style.display = 'block';

    // Wire blur / change handlers (remove old ones by cloning nodes)
    document.querySelectorAll('.filament-edit-field').forEach(el => {
        const clone = el.cloneNode(true);
        el.parentNode.replaceChild(clone, el);
        const event = clone.type === 'color' ? 'change' : 'blur';
        clone.addEventListener(event, () => handleFilamentEditBlur(clone));
    });
}

function closeEditFilamentModal() {
    document.getElementById('editFilamentModal').style.display = 'none';
    loadFilaments();
}

function switchFilamentEditTab(tab) {
    document.querySelectorAll('#editFilamentModal .hm-tab').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    document.getElementById('filEditTab-details').style.display = tab === 'details' ? '' : 'none';
    document.getElementById('filEditTab-opttag').style.display  = tab === 'opttag'  ? '' : 'none';
}

function handleFilamentEditBlur(el) {
    const field = el.dataset.field;
    const id = _editFilamentID;
    if (!field || !id) return;

    let value = el.value;

    // Color picker returns #RRGGBB; Spoolman stores without '#'
    if (field === 'color_hex') value = value.replace(/^#/, '');

    // Integer fields
    const intFields = new Set([
        'settings_extruder_temp', 'settings_bed_temp',
        'nfc_min_print_temp', 'nfc_max_print_temp',
        'nfc_min_bed_temp', 'nfc_max_bed_temp', 'nfc_nominal_length', 'vendor_id',
    ]);
    // Float fields
    const floatFields = new Set([
        'diameter', 'weight', 'spool_weight', 'price', 'density', 'nfc_transmission_distance',
    ]);

    if (intFields.has(field)) {
        value = parseInt(value, 10);
        if (isNaN(value)) return;
    } else if (floatFields.has(field)) {
        value = parseFloat(value);
        if (isNaN(value)) return;
    }

    fetch(`/api/filaments/${id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ field, value }),
    })
    .then(r => {
        if (!r.ok) return r.json().then(j => Promise.reject(j.error || r.statusText));
        // Update in-place so the table reflects the change without a full reload
        const f = _allFilaments.find(x => x.id === id);
        if (f) _applyFilamentFieldUpdate(f, field, value);
    })
    .catch(err => showToast(`Save failed: ${err}`, 'error'));
}

function _applyFilamentFieldUpdate(f, field, value) {
    const nativeKeys = ['name','material','color_hex','multi_color_hexes','diameter',
                        'weight','spool_weight','price','density',
                        'settings_extruder_temp','settings_bed_temp'];
    if (nativeKeys.includes(field)) {
        f[field] = value;
    } else if (field === 'vendor_id') {
        const v = _vendorCache ? _vendorCache.find(x => x.id === value) : null;
        if (v) f.vendor = { id: v.id, name: v.name };
    } else if (field.startsWith('nfc_') || field.startsWith('cal_')) {
        if (!f.extra) f.extra = {};
        f.extra[field] = String(value);
    }
}

// Read helpers for Spoolman extra map (mirrors server-side extraInt/extraStr/extraFloat)
function _extraStr(extra, key) {
    const v = extra[key];
    return (v !== undefined && v !== null) ? String(v) : '';
}
function _extraInt(extra, key) {
    const v = extra[key];
    if (v === undefined || v === null) return 0;
    const n = parseInt(v, 10);
    return isNaN(n) ? 0 : n;
}
function _extraFloat(extra, key) {
    const v = extra[key];
    if (v === undefined || v === null) return 0;
    const n = parseFloat(v);
    return isNaN(n) ? 0 : n;
}

function _setFE(id, value) {
    const el = document.getElementById(id);
    if (el) el.value = value;
}

// ── Clone ────────────────────────────────────────────────────────────────────

function cloneFilament(id) {
    fetch(`/api/filaments/${id}/clone`, { method: 'POST' })
        .then(r => {
            if (!r.ok) return r.json().then(j => Promise.reject(j.error || r.statusText));
            loadFilaments();
            showToast('Filament cloned', 'success');
        })
        .catch(err => showToast(`Clone failed: ${err}`, 'error'));
}

// ── Helpers ─────────────────────────────────────────────────────────────────

function escHtml(s) {
    return String(s)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}

function escAttr(s) {
    return String(s).replace(/"/g, '&quot;').replace(/\x00/g, '-');
}
