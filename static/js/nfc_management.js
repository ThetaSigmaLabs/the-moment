// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u
//
// NFCs tab — tag registry management (Stage 2: Filament sub-tab).
// Binding lives in nfc_tags; Spoolman remains source of truth for filament data.

let nfcsCurrentPayloadTagId = null;

// ─── Sort state & data caches ─────────────────────────────────────────────────

let nfcsSortState = {
    filament: { field: 'label', asc: true },
    spool:    { field: 'label', asc: true },
    location: { field: 'label', asc: true }
};
let nfcsFilamentData = [];
let nfcsSpoolData    = [];
let nfcsLocationData = [];

function nfcsSortTable(kind, field) {
    const s = nfcsSortState[kind];
    if (s.field === field) { s.asc = !s.asc; } else { s.field = field; s.asc = true; }
    nfcsRenderKind(kind);
}

function nfcsSortKeyFor(kind, t, field) {
    switch (field) {
        case 'tag_id': return (t.tag_id || '').toLowerCase();
        case 'label':  return (t.label || '').toLowerCase();
        case 'bound':
            if (kind === 'filament') {
                if (t.filament) return ((t.filament.vendor || '') + ' ' + (t.filament.name || '') + ' ' + (t.filament.material || '')).toLowerCase();
                if (t.bound_entity_id) return 'filament #' + t.bound_entity_id;
                return '';
            }
            if (kind === 'spool') {
                if (t.spool) return ((t.spool.vendor || '') + ' ' + (t.spool.name || '')).toLowerCase();
                if (t.bound_entity_id) return 'spool #' + t.bound_entity_id;
                return '';
            }
            return '';
        case 'spoolman_id':
            const sid = (t.spool && t.spool.id) ? t.spool.id : (t.bound_entity_id || 0);
            return sid;
        case 'status':
            if (t.spool && t.spool.archived) return 'spool archived';
            return (t.status || '').toLowerCase();
        case 'kind':
            return ((t.location && t.location.kind) || '').toLowerCase();
        default: return '';
    }
}

function nfcsSortData(kind, data) {
    const s = nfcsSortState[kind];
    return data.slice().sort(function (a, b) {
        const va = nfcsSortKeyFor(kind, a, s.field);
        const vb = nfcsSortKeyFor(kind, b, s.field);
        const cmp = va < vb ? -1 : va > vb ? 1 : 0;
        return s.asc ? cmp : -cmp;
    });
}

function nfcsUpdateSortHeaders(kind) {
    const s = nfcsSortState[kind];
    const table = document.querySelector('#nfcs-subtab-' + kind + ' .nfcs-table');
    if (!table) return;
    table.querySelectorAll('th[data-sort]').forEach(function (th) {
        const arrow = th.querySelector('.nfcs-sort-arrow');
        if (!arrow) return;
        arrow.textContent = th.dataset.sort === s.field ? (s.asc ? ' ▲' : ' ▼') : ' ↕';
    });
}

function nfcsRenderKind(kind) {
    if (kind === 'filament') nfcsRenderFilamentRows(nfcsFilamentData);
    else if (kind === 'spool') nfcsRenderSpoolRows(nfcsSpoolData);
    else if (kind === 'location') nfcsRenderLocationRows(nfcsLocationData);
    nfcsUpdateSortHeaders(kind);
    const searchEl = document.getElementById('nfcs-' + kind + '-search');
    if (searchEl && searchEl.value) nfcsFilterTable(kind);
}

// Lazy-load hook called by switchNfcsSubTab when a sub-tab is shown.
window.nfcsOnSubTabShown = function (name) {
    if (name === 'filament') nfcsLoadFilamentTags();
    else if (name === 'spool') nfcsLoadSpoolTags();
    else if (name === 'location') nfcsLoadLocationTags();
};

function nfcsEscape(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
        return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
    });
}

function nfcsToast(msg) {
    if (typeof showToast === 'function') showToast(msg); else alert(msg);
}

// Reload whichever NFCs sub-tab is currently visible.
function nfcsReloadActive() {
    const spool = document.getElementById('nfcs-subtab-spool');
    const filament = document.getElementById('nfcs-subtab-filament');
    const location = document.getElementById('nfcs-subtab-location');
    if (filament && filament.style.display !== 'none') nfcsLoadFilamentTags();
    else if (spool && spool.style.display !== 'none') nfcsLoadSpoolTags();
    else if (location && location.style.display !== 'none') nfcsLoadLocationTags();
}

async function nfcsLoadFilamentTags() {
    if (!document.getElementById('nfcs-filament-rows')) return;
    try {
        const res = await fetch('/api/nfc/tags?type=filament');
        const tags = await res.json();
        nfcsFilamentData = Array.isArray(tags) ? tags : [];
        nfcsRenderKind('filament');
    } catch (e) {
        nfcsToast('Failed to load filament tags: ' + e.message);
    }
}

function nfcsRenderFilamentRows(data) {
    const tbody = document.getElementById('nfcs-filament-rows');
    const empty = document.getElementById('nfcs-filament-empty');
    if (!tbody) return;
    const sorted = nfcsSortData('filament', data);
    tbody.innerHTML = '';
    if (sorted.length === 0) { empty.style.display = ''; return; }
    empty.style.display = 'none';
    sorted.forEach(function (t) {
        const tr = document.createElement('tr');
        tr.className = 'nfcs-row-clickable';
        const shortId = t.tag_id.slice(0, 8);
        let bound = '<span style="color:var(--text-secondary);">— unbound —</span>';
        let boundText = '— unbound —';
        if (t.filament) {
            const hex = t.filament.color_hex ? ('#' + String(t.filament.color_hex).replace(/^#/, '')) : '#888';
            const vend = t.filament.vendor ? (nfcsEscape(t.filament.vendor) + ' · ') : '';
            bound = '<span class="nfcs-swatch" style="background:' + hex + '"></span>' +
                vend + nfcsEscape(t.filament.name || ('Filament #' + t.filament.id)) +
                ' <span style="color:var(--text-secondary);">(' + nfcsEscape(t.filament.material || '') + ')</span>';
            boundText = (t.filament.vendor ? t.filament.vendor + ' · ' : '') +
                (t.filament.name || ('Filament #' + t.filament.id)) + ' (' + (t.filament.material || '') + ')';
        } else if (t.bound_entity_id) {
            bound = '<span style="color:var(--text-secondary);">filament #' + t.bound_entity_id + ' (not in Spoolman)</span>';
            boundText = 'filament #' + t.bound_entity_id + ' (not in Spoolman)';
        }
        const subjectLbl = t.label || '';
        const subjectBound = (boundText && boundText !== '— unbound —') ? boundText : '';
        nfcsTagSubjectCache[t.tag_id] = subjectLbl && subjectBound ? subjectLbl + ' - ' + subjectBound : (subjectLbl || subjectBound);
        const label = t.label ? nfcsEscape(t.label) : '<span style="color:var(--text-secondary);">—</span>';
        tr.innerHTML =
            '<td><input type="checkbox" class="nfcs-fil-check" data-tag-id="' + nfcsEscape(t.tag_id) + '" onclick="event.stopPropagation()"></td>' +
            '<td class="nfcs-tagid" title="' + nfcsEscape(t.tag_id) + '">' + nfcsEscape(shortId) + '…</td>' +
            '<td>' + label + '</td>' +
            '<td>' + bound + '</td>' +
            '<td style="white-space:nowrap;">' +
            '<button class="nfcs-rowbtn" style="background:#7c3aed;color:#fff;" onclick="event.stopPropagation(); nfcsShowPayload(\'' + nfcsEscape(t.tag_id) + '\')">Write</button>' +
            '<button class="nfcs-rowbtn" style="background:#ef4444;color:#fff;" onclick="event.stopPropagation(); nfcsDeleteTag(\'' + nfcsEscape(t.tag_id) + '\')">Delete</button>' +
            '</td>';
        tr.title = 'Click to edit';
        tr.addEventListener('click', function () { nfcsOpenEdit(t.tag_id, 'filament', t.label || '', boundText, t.bound_entity_id); });
        tbody.appendChild(tr);
    });
}

async function nfcsLoadSpoolTags() {
    if (!document.getElementById('nfcs-spool-rows')) return;
    try {
        const res = await fetch('/api/nfc/tags?type=spool');
        const tags = await res.json();
        nfcsSpoolData = Array.isArray(tags) ? tags : [];
        nfcsRenderKind('spool');
    } catch (e) {
        nfcsToast('Failed to load spool tags: ' + e.message);
    }
}

function nfcsRenderSpoolRows(data) {
    const tbody = document.getElementById('nfcs-spool-rows');
    const empty = document.getElementById('nfcs-spool-empty');
    if (!tbody) return;
    const unboundOnly = document.getElementById('nfcs-spool-unbound-only') && document.getElementById('nfcs-spool-unbound-only').checked;
    let filtered = unboundOnly ? data.filter(function (t) { return !t.bound_entity_id; }) : data;
    const sorted = nfcsSortData('spool', filtered);
    tbody.innerHTML = '';
    if (sorted.length === 0) { empty.style.display = ''; return; }
    empty.style.display = 'none';
    sorted.forEach(function (t) {
        const tr = document.createElement('tr');
        tr.className = 'nfcs-row-clickable';
        const shortId = t.tag_id.slice(0, 8);
        let bound = '<span style="color:var(--text-secondary);">— unbound —</span>';
        let boundText = '— unbound —';
        if (t.spool) {
            const hex = t.spool.color_hex ? ('#' + String(t.spool.color_hex).replace(/^#/, '')) : '#888';
            const vend = t.spool.vendor ? (nfcsEscape(t.spool.vendor) + ' · ') : '';
            const loc = t.spool.location ? (' · 📍 ' + nfcsEscape(t.spool.location)) : '';
            const wt = (t.spool.remaining_weight != null) ? (' · ' + Math.round(t.spool.remaining_weight) + 'g') : '';
            bound = '<span class="nfcs-swatch" style="background:' + hex + '"></span>' +
                '[' + t.spool.id + '] ' + vend + nfcsEscape(t.spool.name || ('Spool #' + t.spool.id)) +
                ' <span style="color:var(--text-secondary);">(' + nfcsEscape(t.spool.material || '') + wt + loc + ')</span>';
            boundText = '[' + t.spool.id + '] ' + (t.spool.vendor ? t.spool.vendor + ' · ' : '') +
                (t.spool.name || ('Spool #' + t.spool.id)) + ' (' + (t.spool.material || '') +
                (t.spool.location ? ' · ' + t.spool.location : '') + ')';
        } else if (t.bound_entity_id) {
            bound = '<span style="color:var(--text-secondary);">spool #' + t.bound_entity_id + ' (not in Spoolman)</span>';
            boundText = 'spool #' + t.bound_entity_id + ' (not in Spoolman)';
        }
        const spSubjectLbl = t.label || '';
        const spSubjectBound = (boundText && boundText !== '— unbound —') ? boundText : '';
        nfcsTagSubjectCache[t.tag_id] = spSubjectLbl && spSubjectBound ? spSubjectLbl + ' - ' + spSubjectBound : (spSubjectLbl || spSubjectBound);
        const archived = t.spool && t.spool.archived;
        const statusTxt = archived ? 'spool archived' : t.status;
        const label = t.label ? nfcsEscape(t.label) : '<span style="color:var(--text-secondary);">—</span>';
        let actions =
            '<button class="nfcs-rowbtn" style="background:#7c3aed;color:#fff;" onclick="event.stopPropagation(); nfcsShowPayload(\'' + nfcsEscape(t.tag_id) + '\')">Write</button>';
        if (t.spool && !archived) {
            actions += '<button class="nfcs-rowbtn" style="background:#b45309;color:#fff;" onclick="event.stopPropagation(); nfcsArchiveSpool(' + t.spool.id + ')" title="Zero remaining weight and move the Spoolman spool to Trash">Archive spool</button>';
        }
        actions += '<button class="nfcs-rowbtn" style="background:#ef4444;color:#fff;" onclick="event.stopPropagation(); nfcsDeleteTag(\'' + nfcsEscape(t.tag_id) + '\')">Delete</button>';
        const spoolmanId = (t.spool && t.spool.id) ? t.spool.id : (t.bound_entity_id || null);
        const spoolmanIdCell = spoolmanId != null ? String(spoolmanId) : '<span style="color:var(--text-secondary);">—</span>';
        tr.innerHTML =
            '<td><input type="checkbox" class="nfcs-spo-check" data-tag-id="' + nfcsEscape(t.tag_id) + '" onclick="event.stopPropagation()"></td>' +
            '<td class="nfcs-tagid" title="' + nfcsEscape(t.tag_id) + '">' + nfcsEscape(shortId) + '…</td>' +
            '<td>' + label + '</td>' +
            '<td>' + spoolmanIdCell + '</td>' +
            '<td>' + bound + '</td>' +
            '<td>' + nfcsEscape(statusTxt) + '</td>' +
            '<td style="white-space:nowrap;">' + actions + '</td>';
        tr.title = 'Click to edit';
        tr.addEventListener('click', function () { nfcsOpenEdit(t.tag_id, 'spool', t.label || '', boundText, t.bound_entity_id); });
        tbody.appendChild(tr);
    });
}

function nfcsFilterTable(kind) {
    const term = (document.getElementById('nfcs-' + kind + '-search').value || '').toLowerCase();
    document.querySelectorAll('#nfcs-' + kind + '-rows tr').forEach(function (tr) {
        tr.style.display = tr.textContent.toLowerCase().includes(term) ? '' : 'none';
    });
}

function nfcsToggleAll(kind, checked) {
    document.querySelectorAll('#nfcs-' + kind + '-rows .nfcs-' + kind.slice(0, 3) + '-check').forEach(function (cb) {
        cb.checked = checked;
    });
}

function nfcsCloseModal(id) {
    const el = document.getElementById(id);
    if (el) el.style.display = 'none';
}

// ─── Add filament tag ──────────────────────────────────────────────────────────

// Four modes: link existing, author new, add unbound, or look up via OpenPrintTag source.
function nfcsSetAddMode(mode) {
    document.getElementById('nfcs-add-link-section').style.display = mode === 'link' ? '' : 'none';
    document.getElementById('nfcs-add-author-section').style.display = mode === 'author' ? '' : 'none';
    document.getElementById('nfcs-add-unbound-section').style.display = mode === 'unbound' ? '' : 'none';
    document.getElementById('nfcs-add-openprinttag-section').style.display = mode === 'openprinttag' ? '' : 'none';
    document.getElementById('nfcs-mode-link').classList.toggle('active', mode === 'link');
    document.getElementById('nfcs-mode-author').classList.toggle('active', mode === 'author');
    document.getElementById('nfcs-mode-unbound').classList.toggle('active', mode === 'unbound');
    document.getElementById('nfcs-mode-openprinttag').classList.toggle('active', mode === 'openprinttag');
}

// Searchable filament picker (mirrors the Spool picker; reuses nfcsMatchSearch).
let nfcsFilamentPickerData = [];

function nfcsFilamentOptionText(f) {
    const vendor = f.vendor ? (f.vendor.name + ' · ') : '';
    return '[' + f.id + '] ' + vendor + (f.name || 'Filament') + ' · ' + (f.material || '');
}

function nfcsRenderFilamentPicker(filter) {
    const box = document.getElementById('nfcs-fil-options');
    const selectedId = document.getElementById('nfcs-fil-link-select').value;
    box.innerHTML = '';
    const matches = nfcsFilamentPickerData.filter(function (f) {
        return nfcsMatchSearch(nfcsFilamentOptionText(f), filter);
    });
    if (matches.length === 0) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">No filaments match.</div>';
        return;
    }
    matches.forEach(function (f) {
        const div = document.createElement('div');
        div.className = 'nfcs-pick' + (String(f.id) === String(selectedId) ? ' selected' : '');
        const hex = f.color_hex ? ('#' + String(f.color_hex).replace(/^#/, '')) : '#888';
        div.innerHTML = '<span class="nfcs-swatch" style="background:' + hex + '"></span>' + nfcsEscape(nfcsFilamentOptionText(f));
        div.addEventListener('click', function () { nfcsSelectFilament(f.id); });
        box.appendChild(div);
    });
}

function nfcsFilterFilamentPicker() {
    nfcsRenderFilamentPicker(document.getElementById('nfcs-fil-search').value);
}

function nfcsSelectFilament(id) {
    document.getElementById('nfcs-fil-link-select').value = id;
    const f = nfcsFilamentPickerData.find(function (x) { return String(x.id) === String(id); });
    document.getElementById('nfcs-fil-selected').textContent = f ? ('Selected: ' + nfcsFilamentOptionText(f)) : '';
    nfcsRenderFilamentPicker(document.getElementById('nfcs-fil-search').value);
}

async function nfcsOpenAddFilament() {
    document.getElementById('nfcs-fil-label').value = '';
    ['manufacturer', 'material', 'colorname', 'colorhex', 'diameter', 'density', 'weight', 'price'].forEach(function (f) {
        const el = document.getElementById('nfcs-fil-' + f);
        if (el) el.value = '';
    });
    nfcsSetAddMode('link');
    document.getElementById('nfcs-fil-link-select').value = '';
    document.getElementById('nfcs-fil-selected').textContent = '';
    document.getElementById('nfcs-fil-search').value = '';
    // Reset OPT section state
    document.getElementById('nfcs-opt-search').value = '';
    document.getElementById('nfcs-opt-results').innerHTML = '';
    document.getElementById('nfcs-opt-selected-ref').value = '';
    document.getElementById('nfcs-opt-selected-source-id').value = '';
    document.getElementById('nfcs-opt-selected-info').textContent = '';
    document.getElementById('nfcs-opt-match-prompt').style.display = 'none';
    document.getElementById('nfcs-opt-variant-section').style.display = 'none';
    document.getElementById('nfcs-opt-selected-variant').value = '';
    nfcsOPTLastResults = [];
    nfcsOPTSelectedFilament = null;
    document.getElementById('nfcs-add-filament-overlay').style.display = 'flex';

    const box = document.getElementById('nfcs-fil-options');
    box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">Loading…</div>';
    try {
        const res = await fetch('/api/filaments');
        const filaments = await res.json();
        nfcsFilamentPickerData = Array.isArray(filaments) ? filaments : [];
        nfcsRenderFilamentPicker('');
    } catch (e) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:#ef4444;">Failed to load filaments</div>';
    }
    nfcsLoadOPTSources();
}

async function nfcsSubmitAddFilament() {
    const label = document.getElementById('nfcs-fil-label').value.trim();
    const authoring = document.getElementById('nfcs-add-author-section').style.display !== 'none';
    const linking = document.getElementById('nfcs-add-link-section').style.display !== 'none';
    const optMode = document.getElementById('nfcs-add-openprinttag-section').style.display !== 'none';

    if (optMode) {
        const ref = document.getElementById('nfcs-opt-selected-ref').value;
        const sourceId = parseInt(document.getElementById('nfcs-opt-selected-source-id').value, 10);
        if (!ref || !sourceId) { nfcsToast('Select a filament from the search results.'); return; }
        if (!ref.includes('/variants/')) { nfcsToast('Select a colour variant before creating the tag.'); return; }
        const actionEl = document.querySelector('input[name="nfcs-opt-action"]:checked');
        const action = actionEl ? actionEl.value : 'create_new';
        const optBody = { source_id: sourceId, source_ref: ref, action: action };
        if (label) optBody.label = label;
        if (action === 'update_existing') {
            const fid = parseInt(document.getElementById('nfcs-opt-match-filament-id').value, 10);
            if (fid > 0) optBody.filament_id = fid;
        }
        try {
            const res = await fetch('/api/nfc/openprinttag-tag', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(optBody)
            });
            const data = await res.json();
            if (!res.ok) { nfcsToast('Create failed: ' + (data.error || res.statusText)); return; }
            nfcsCloseModal('nfcs-add-filament-overlay');
            await nfcsLoadFilamentTags();
            if (data.tag && data.tag.tag_id) {
                nfcsRenderPayload(data.tag.tag_id, data.tag_url, data.qr_code_base64, data.note, nfcsTagSubjectCache[data.tag.tag_id] || '');
            }
        } catch (e) {
            nfcsToast('Create error: ' + e.message);
        }
        return;
    }

    const body = { tag_type: 'filament' };
    if (label) body.label = label;

    if (authoring) {
        const num = function (id) { const v = parseFloat(document.getElementById(id).value); return isNaN(v) ? 0 : v; };
        const spec = {
            manufacturer: document.getElementById('nfcs-fil-manufacturer').value.trim(),
            material: document.getElementById('nfcs-fil-material').value.trim(),
            color_name: document.getElementById('nfcs-fil-colorname').value.trim(),
            color_hex: document.getElementById('nfcs-fil-colorhex').value.trim(),
            diameter_mm: num('nfcs-fil-diameter'),
            density: num('nfcs-fil-density'),
            default_weight_g: num('nfcs-fil-weight'),
            default_price: num('nfcs-fil-price')
        };
        if (!spec.material && !spec.color_name) {
            nfcsToast('Material or color is required to author a new filament.');
            return;
        }
        body.spec = spec;
    } else if (linking) {
        const fid = parseInt(document.getElementById('nfcs-fil-link-select').value, 10);
        if (!fid) { nfcsToast('Choose a Spoolman filament to link, or switch to "Add unbound".'); return; }
        body.filament_id = fid;
    }
    // else: "Add unbound" — send neither filament_id nor spec; backend creates an unbound tag.

    try {
        const res = await fetch('/api/nfc/tags', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });
        const data = await res.json();
        if (!res.ok) { nfcsToast('Create failed: ' + (data.error || res.statusText)); return; }
        nfcsCloseModal('nfcs-add-filament-overlay');
        await nfcsLoadFilamentTags();
        if (data.tag && data.tag.tag_id) {
            nfcsRenderPayload(data.tag.tag_id, data.tag_url, data.qr_code_base64, data.note, nfcsTagSubjectCache[data.tag.tag_id] || '');
        }
    } catch (e) {
        nfcsToast('Create error: ' + e.message);
    }
}

// ─── Edit tag (inline label, blur-to-save, rebind) ────────────────────────────

// Tracks the tag being edited so blur-to-save, Write/Delete, rebind, and location-kind act on it.
let nfcsEditState = { tagId: null, tagType: null, orig: '', boundDesc: '', boundEntityId: null, locationKind: null, locationData: null };

// Open the Edit dialog.
// boundDesc: plain-text binding summary (spool/filament) or ignored (location).
// boundEntityId: current bound entity id or null (spool/filament only).
// locationKind: current location_kind string or null (location tags only).
// locationData: nfcLocationSummary object or null (location tags only).
function nfcsOpenEdit(tagId, tagType, label, boundDesc, boundEntityId, locationKind, locationData) {
    nfcsEditState = { tagId: tagId, tagType: tagType, orig: label || '', boundDesc: boundDesc || '', boundEntityId: boundEntityId || null, locationKind: locationKind || null, locationData: locationData || null };
    const titles = { spool: '🧵 Spool tag', filament: '🧪 Filament tag', location: '📍 Location tag' };
    document.getElementById('nfcs-edit-title').textContent = titles[tagType] || '🏷️ Tag';
    document.getElementById('nfcs-edit-tagid').textContent = tagId;
    document.getElementById('nfcs-edit-savestate').textContent = '';
    const input = document.getElementById('nfcs-edit-label');
    input.value = label || '';

    const isLocation = tagType === 'location';

    // Location tabs: only for location tags (replaces old nfcs-location-kind-section).
    const locTabs = document.getElementById('nfcs-loc-tabs');
    if (locTabs) locTabs.style.display = isLocation ? '' : 'none';
    if (isLocation) nfcsLocOpenDetails(locationKind, locationData);

    // Rebind section: hidden for location tags (Details tab owns the binding UX).
    const rebindSection = document.getElementById('nfcs-rebind-section');
    if (rebindSection) {
        if (isLocation) {
            rebindSection.style.display = 'none';
        } else {
            rebindSection.style.display = '';
            document.getElementById('nfcs-rebind-current').textContent = boundDesc || '— unbound —';
            document.getElementById('nfcs-rebind-unbind-btn').style.display = boundEntityId ? '' : 'none';
            document.getElementById('nfcs-rebind-spool-picker').style.display = 'none';
            document.getElementById('nfcs-rebind-filament-picker').style.display = 'none';
        }
    }

    document.getElementById('nfcs-edit-overlay').style.display = 'flex';
    setTimeout(function () { input.focus(); input.select(); }, 30);
}

// Save the label on blur (or Enter). No-ops when unchanged.
async function nfcsEditLabelSave() {
    if (!nfcsEditState.tagId) return;
    const next = document.getElementById('nfcs-edit-label').value.trim();
    if (next === (nfcsEditState.orig || '')) return;
    const state = document.getElementById('nfcs-edit-savestate');
    try {
        const res = await fetch('/api/nfc/tags/' + encodeURIComponent(nfcsEditState.tagId) + '/label', {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ label: next })
        });
        const data = await res.json().catch(function () { return {}; });
        if (!res.ok) {
            state.style.color = '#ef4444';
            state.textContent = '⚠ ' + (data.error || 'Save failed');
            return;
        }
        nfcsEditState.orig = next;
        state.style.color = 'var(--text-secondary)';
        state.textContent = '✓ Saved';
        nfcsReloadActive();
    } catch (e) {
        state.style.color = '#ef4444';
        state.textContent = '⚠ ' + e.message;
    }
}

// ─── Location tag edit tabs ────────────────────────────────────────────────────

// Parse "{PrinterName} - T{N}" label → {printerName, toolheadIdx} or null.
function nfcsParseToolheadLabel(label) {
    if (!label) return null;
    const m = label.match(/^(.+) - T(\d+)$/);
    return m ? { printerName: m[1], toolheadIdx: parseInt(m[2], 10) } : null;
}

// Switch between Details and Spools tabs inside the edit modal.
function nfcsLocShowTab(tab) {
    document.getElementById('nfcs-loc-tab-details').style.display = tab === 'details' ? '' : 'none';
    document.getElementById('nfcs-loc-tab-spools').style.display  = tab === 'spools'  ? '' : 'none';
    document.getElementById('nfcs-loc-tab-det-btn').classList.toggle('active', tab === 'details');
    document.getElementById('nfcs-loc-tab-sp-btn').classList.toggle('active',  tab === 'spools');
}

// Populate the location edit tabs when opening the edit modal for a location tag.
async function nfcsLocOpenDetails(kind, locationData) {
    // Reset to summary view (change form closed).
    document.getElementById('nfcs-loc-change-form').style.display = 'none';
    document.getElementById('nfcs-loc-summary').style.display = '';
    document.getElementById('nfcs-edit-loc-kind-state').textContent = '';

    nfcsLocRenderSummary(kind);
    await nfcsLocRenderSpoolsTab(locationData);
    nfcsLocShowTab('details');
}

// Populate summary text from current state.
function nfcsLocRenderSummary(kind) {
    const el = document.getElementById('nfcs-loc-summary-text');
    if (!el) return;
    const label = nfcsEditState.orig || '— no label —';
    const emoji = { toolhead: '🖨️', inventory: '📦', archive: '🗄️', trash: '🗑️' }[kind] || '📍';
    el.textContent = emoji + ' ' + label;
}

// Toggle the change form open/closed. Also serves as Cancel handler.
async function nfcsLocChangeToggle() {
    const form = document.getElementById('nfcs-loc-change-form');
    const summary = document.getElementById('nfcs-loc-summary');
    if (form.style.display !== 'none') {
        form.style.display = 'none';
        summary.style.display = '';
        return;
    }
    summary.style.display = 'none';
    document.getElementById('nfcs-edit-loc-kind-state').textContent = '';
    document.getElementById('nfcs-edit-loc-kind').value = nfcsEditState.locationKind || 'toolhead';
    nfcsLocChangeFormKindChange();
    if ((nfcsEditState.locationKind || 'toolhead') === 'toolhead') {
        await nfcsLocLoadPrinters();
    }
    form.style.display = '';
}

// Load printers into the edit-modal printer select and pre-select from current label.
async function nfcsLocLoadPrinters() {
    const printerSel = document.getElementById('nfcs-edit-loc-printer');
    printerSel.innerHTML = '<option value="">Loading…</option>';
    try {
        const res = await fetch('/api/printers');
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        nfcsPrinterList = [];
        Object.entries(data.printers || {}).forEach(function ([id, p]) {
            if (p) nfcsPrinterList.push({ id: id, name: p.name, toolheads: p.toolheads || 1 });
        });
        nfcsPrinterList.sort(function (a, b) { return a.name.localeCompare(b.name); });
        printerSel.innerHTML = nfcsPrinterList.length
            ? nfcsPrinterList.map(function (p) {
                return '<option value="' + nfcsEscape(p.id) + '" data-name="' + nfcsEscape(p.name) + '" data-toolheads="' + p.toolheads + '">' + nfcsEscape(p.name) + '</option>';
              }).join('')
            : '<option value="">No printers configured</option>';
    } catch (e) {
        printerSel.innerHTML = '<option value="">Failed to load printers</option>';
    }
    const parsed = nfcsParseToolheadLabel(nfcsEditState.orig);
    if (parsed) {
        Array.from(printerSel.options).forEach(function (o) {
            if (o.dataset.name === parsed.printerName) o.selected = true;
        });
    }
    const selOpt = printerSel.options[printerSel.selectedIndex];
    const toolheads = selOpt ? parseInt(selOpt.dataset.toolheads, 10) || 1 : 1;
    const idxSel = document.getElementById('nfcs-edit-loc-toolhead-idx');
    idxSel.innerHTML = Array.from({ length: toolheads }, function (_, i) {
        return '<option value="' + i + '">T' + i + '</option>';
    }).join('');
    if (parsed) idxSel.value = String(parsed.toolheadIdx);
}

// Show/hide toolhead section when Kind changes in the change form (no auto-save).
function nfcsLocChangeFormKindChange() {
    const kind = document.getElementById('nfcs-edit-loc-kind').value;
    document.getElementById('nfcs-edit-loc-toolhead-section').style.display = kind === 'toolhead' ? '' : 'none';
}

// Called when the Printer select changes in the edit modal — rebuilds toolhead idx and auto-fills label.
function nfcsLocDetailsPrinterChange() {
    const sel = document.getElementById('nfcs-edit-loc-printer');
    const opt = sel && sel.options[sel.selectedIndex];
    const toolheads = opt ? parseInt(opt.dataset.toolheads, 10) || 1 : 1;
    const idxSel = document.getElementById('nfcs-edit-loc-toolhead-idx');
    if (!idxSel) return;
    idxSel.innerHTML = Array.from({ length: toolheads }, function (_, i) {
        return '<option value="' + i + '">T' + i + '</option>';
    }).join('');
    nfcsLocDetailsToolheadChange();
}

// Auto-fills the label field when toolhead selection changes in the edit modal.
function nfcsLocDetailsToolheadChange() {
    const printerSel = document.getElementById('nfcs-edit-loc-printer');
    const opt = printerSel && printerSel.options[printerSel.selectedIndex];
    const printerName = opt ? opt.dataset.name : '';
    const idx = document.getElementById('nfcs-edit-loc-toolhead-idx').value;
    if (printerName) document.getElementById('nfcs-edit-label').value = printerName + ' - T' + idx;
}

// Save button handler for the location change form.
async function nfcsLocChangeSave() {
    if (!nfcsEditState.tagId) return;
    const newKind = document.getElementById('nfcs-edit-loc-kind').value;
    const prevKind = nfcsEditState.locationKind;
    const stateEl = document.getElementById('nfcs-edit-loc-kind-state');

    // Changing away from toolhead with a spool assigned: confirm + unmap first.
    if (prevKind === 'toolhead' && newKind !== 'toolhead' && nfcsEditState.locationData && nfcsEditState.locationData.spool_id > 0) {
        const spoolName = nfcsEditState.locationData.spool_name || ('Spool #' + nfcsEditState.locationData.spool_id);
        if (!confirm('Changing kind will send "' + spoolName + '" to inventory. Proceed?')) return;
        const parsed = nfcsParseToolheadLabel(nfcsEditState.orig);
        if (parsed) {
            const r = await fetch('/api/map_toolhead', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ printer_name: parsed.printerName, toolhead_id: parsed.toolheadIdx, spool_id: 0 })
            });
            if (!r.ok) {
                stateEl.style.color = '#ef4444';
                stateEl.textContent = '⚠ Unmap failed';
                return;
            }
            nfcsEditState.locationData = null;
        }
    }

    // For toolhead kind: save the label derived from printer+toolhead selection if it changed.
    if (newKind === 'toolhead') {
        const printerSel = document.getElementById('nfcs-edit-loc-printer');
        const opt = printerSel && printerSel.options[printerSel.selectedIndex];
        const printerName = opt ? opt.dataset.name : '';
        const idx = document.getElementById('nfcs-edit-loc-toolhead-idx').value;
        const newLabel = printerName ? printerName + ' - T' + idx : '';
        if (newLabel && newLabel !== nfcsEditState.orig) {
            stateEl.style.color = 'var(--text-secondary)';
            stateEl.textContent = 'Saving…';
            try {
                const lr = await fetch('/api/nfc/tags/' + encodeURIComponent(nfcsEditState.tagId) + '/label', {
                    method: 'PATCH',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ label: newLabel })
                });
                if (!lr.ok) {
                    const ld = await lr.json().catch(function () { return {}; });
                    stateEl.style.color = '#ef4444';
                    stateEl.textContent = '⚠ ' + (ld.error || 'Label save failed');
                    return;
                }
                nfcsEditState.orig = newLabel;
                document.getElementById('nfcs-edit-label').value = newLabel;
            } catch (e) {
                stateEl.style.color = '#ef4444';
                stateEl.textContent = '⚠ ' + e.message;
                return;
            }
        }
    }

    // Save kind if changed.
    if (newKind !== prevKind) {
        stateEl.style.color = 'var(--text-secondary)';
        stateEl.textContent = 'Saving…';
        try {
            const res = await fetch('/api/nfc/tags/' + encodeURIComponent(nfcsEditState.tagId) + '/location-kind', {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ location_kind: newKind })
            });
            const d = await res.json().catch(function () { return {}; });
            if (!res.ok) {
                stateEl.style.color = '#ef4444';
                stateEl.textContent = '⚠ ' + (d.error || 'Save failed');
                return;
            }
            nfcsEditState.locationKind = newKind;
        } catch (e) {
            stateEl.style.color = '#ef4444';
            stateEl.textContent = '⚠ ' + e.message;
            return;
        }
    }

    // Success: collapse form, update summary, refresh spools.
    document.getElementById('nfcs-loc-change-form').style.display = 'none';
    document.getElementById('nfcs-loc-summary').style.display = '';
    nfcsLocRenderSummary(nfcsEditState.locationKind);
    await nfcsLocRenderSpoolsTab(nfcsEditState.locationData);
    nfcsReloadActive();
}

// Render the Spools tab content for the edit modal.
async function nfcsLocRenderSpoolsTab(locationData) {
    const el = document.getElementById('nfcs-loc-spools-content');
    if (!el) return;
    const kind = nfcsEditState.locationKind || 'toolhead';

    if (kind !== 'toolhead') {
        // For inventory/archive/trash: show spools from Spoolman whose location matches this tag's label.
        const label = nfcsEditState.orig;
        if (!label) {
            el.innerHTML = '<p class="help-text" style="margin:8px 0;">Set a label first to see spools at this location.</p>';
            return;
        }
        el.innerHTML = '<p class="help-text" style="margin:8px 0;">Loading…</p>';
        try {
            const res = await fetch('/api/spools');
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const spools = await res.json();
            const here = Array.isArray(spools) ? spools.filter(function (s) {
                return s.location && s.location.toLowerCase() === label.toLowerCase();
            }) : [];
            if (here.length === 0) {
                el.innerHTML = '<p class="help-text" style="margin:8px 0;">No spools at this location.</p>';
                return;
            }
            el.innerHTML = here.map(function (s) {
                const colorHex = (s.filament && s.filament.color_hex) ? ('#' + String(s.filament.color_hex).replace(/^#/, '')) : '#888';
                const name = s.name || ('Spool #' + s.id);
                const material = s.material || '';
                return '<div style="display:flex;align-items:center;gap:8px;padding:8px;background:var(--surface-secondary);border-radius:6px;margin-bottom:4px;">' +
                    '<span class="nfcs-swatch" style="background:' + colorHex + ';flex-shrink:0;"></span>' +
                    '<div style="flex:1;">' +
                    '<div style="font-size:0.9em;">' + nfcsEscape(name) + '</div>' +
                    (material ? '<div style="font-size:0.78em;color:var(--text-secondary);">' + nfcsEscape(material) + '</div>' : '') +
                    '</div></div>';
            }).join('');
        } catch (e) {
            el.innerHTML = '<p class="help-text" style="margin:8px 0;color:#ef4444;">Failed to load: ' + nfcsEscape(e.message) + '</p>';
        }
        return;
    }

    if (!locationData || !locationData.spool_id) {
        el.innerHTML = '<p class="help-text" style="margin:8px 0;">No spool assigned to this toolhead location.</p>';
        return;
    }
    const hex = locationData.color_hex ? ('#' + String(locationData.color_hex).replace(/^#/, '')) : '#888';
    const spoolName = locationData.spool_name || ('Spool #' + locationData.spool_id);
    el.innerHTML =
        '<div style="display:flex;align-items:center;gap:8px;padding:8px;background:var(--surface-secondary);border-radius:6px;">' +
        '<span class="nfcs-swatch" style="background:' + hex + ';flex-shrink:0;"></span>' +
        '<div style="flex:1;">' +
        '<div style="font-size:0.9em;">' + nfcsEscape(spoolName) + '</div>' +
        (locationData.material ? '<div style="font-size:0.78em;color:var(--text-secondary);">' + nfcsEscape(locationData.material) + '</div>' : '') +
        '</div>' +
        '<button class="nfcs-rowbtn" style="background:#f59e0b;color:#fff;" onclick="nfcsLocUnbind()">Send to inventory</button>' +
        '</div>';
}

// Unmap the spool from this toolhead location and send it to inventory.
async function nfcsLocUnbind() {
    const ld = nfcsEditState.locationData;
    if (!ld || !ld.spool_id) return;
    const spoolName = ld.spool_name || ('Spool #' + ld.spool_id);
    if (!confirm('Send "' + spoolName + '" to inventory?')) return;
    const parsed = nfcsParseToolheadLabel(nfcsEditState.orig);
    if (!parsed) { alert('Cannot determine toolhead from label.'); return; }
    try {
        const res = await fetch('/api/map_toolhead', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ printer_name: parsed.printerName, toolhead_id: parsed.toolheadIdx, spool_id: 0 })
        });
        if (!res.ok) {
            const d = await res.json().catch(function () { return {}; });
            alert('Unmap failed: ' + (d.error || res.statusText));
            return;
        }
        nfcsEditState.locationData = null;
        await nfcsLocRenderSpoolsTab(null);
        nfcsReloadActive();
    } catch (e) {
        alert('Error: ' + e.message);
    }
}

// Write button inside the Edit dialog → show the QR/URL payload for the same tag.
function nfcsEditWrite() {
    if (nfcsEditState.tagId) nfcsShowPayload(nfcsEditState.tagId);
}

// Delete button inside the Edit dialog.
async function nfcsEditDelete() {
    const tagId = nfcsEditState.tagId;
    if (!tagId) return;
    if (!confirm('Delete this NFC tag from the registry?\n\nThe bound Spoolman record is NOT affected.')) return;
    try {
        const res = await fetch('/api/nfc/tags/' + encodeURIComponent(tagId), { method: 'DELETE' });
        if (!res.ok) { const d = await res.json().catch(function () { return {}; }); nfcsToast(d.error || 'Delete failed'); return; }
        nfcsCloseModal('nfcs-edit-overlay');
        nfcsReloadActive();
    } catch (e) {
        nfcsToast('Delete error: ' + e.message);
    }
}

// ─── Rebind / unbind (Edit modal binding section) ─────────────────────────────

let nfcsRebindPickerData = [];

// Subject string cache: tag_id → "<Label> - <Bound/Kind>" — populated at list render time.
// Used by list-row Write buttons so they don't read stale nfcsEditState.
let nfcsTagSubjectCache = {};

// Toggle the appropriate picker open/closed; loads Spoolman data on first open.
async function nfcsRebindToggle() {
    const tagType = nfcsEditState.tagType;
    const spPicker = document.getElementById('nfcs-rebind-spool-picker');
    const filPicker = document.getElementById('nfcs-rebind-filament-picker');

    const activePicker = tagType === 'spool' ? spPicker : filPicker;
    if (activePicker.style.display !== 'none') {
        activePicker.style.display = 'none';
        return;
    }

    if (tagType === 'spool') {
        spPicker.style.display = '';
        document.getElementById('nfcs-rebind-sp-search').value = '';
        document.getElementById('nfcs-rebind-sp-select').value = '';
        document.getElementById('nfcs-rebind-sp-selected').textContent = '';
        const box = document.getElementById('nfcs-rebind-sp-options');
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">Loading…</div>';
        try {
            const res = await fetch('/api/nfc/spools');
            const spools = await res.json();
            nfcsRebindPickerData = Array.isArray(spools) ? spools : [];
            nfcsRenderRebindSpoolPicker('');
        } catch (e) {
            box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:#ef4444;">Failed to load spools</div>';
        }
    } else if (tagType === 'filament') {
        filPicker.style.display = '';
        document.getElementById('nfcs-rebind-fil-search').value = '';
        document.getElementById('nfcs-rebind-fil-select').value = '';
        document.getElementById('nfcs-rebind-fil-selected').textContent = '';
        const box = document.getElementById('nfcs-rebind-fil-options');
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">Loading…</div>';
        try {
            const res = await fetch('/api/filaments');
            const filaments = await res.json();
            nfcsRebindPickerData = Array.isArray(filaments) ? filaments : [];
            nfcsRenderRebindFilamentPicker('');
        } catch (e) {
            box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:#ef4444;">Failed to load filaments</div>';
        }
    }
}

function nfcsRenderRebindSpoolPicker(filter) {
    const box = document.getElementById('nfcs-rebind-sp-options');
    const selectedId = document.getElementById('nfcs-rebind-sp-select').value;
    box.innerHTML = '';
    const matches = nfcsRebindPickerData.filter(function (s) { return nfcsMatchSearch(nfcsSpoolOptionText(s), filter); });
    if (matches.length === 0) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">No spools match.</div>';
        return;
    }
    matches.forEach(function (s) {
        const div = document.createElement('div');
        div.className = 'nfcs-pick' + (String(s.id) === String(selectedId) ? ' selected' : '');
        const hex = s.color_hex ? ('#' + String(s.color_hex).replace(/^#/, '')) : '#888';
        div.innerHTML = '<span class="nfcs-swatch" style="background:' + hex + '"></span>' + nfcsEscape(nfcsSpoolOptionText(s));
        div.addEventListener('click', function () {
            document.getElementById('nfcs-rebind-sp-select').value = s.id;
            document.getElementById('nfcs-rebind-sp-selected').textContent = 'Selected: ' + nfcsSpoolOptionText(s);
            nfcsRenderRebindSpoolPicker(document.getElementById('nfcs-rebind-sp-search').value);
        });
        box.appendChild(div);
    });
}

function nfcsRenderRebindFilamentPicker(filter) {
    const box = document.getElementById('nfcs-rebind-fil-options');
    const selectedId = document.getElementById('nfcs-rebind-fil-select').value;
    box.innerHTML = '';
    const matches = nfcsRebindPickerData.filter(function (f) { return nfcsMatchSearch(nfcsFilamentOptionText(f), filter); });
    if (matches.length === 0) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">No filaments match.</div>';
        return;
    }
    matches.forEach(function (f) {
        const div = document.createElement('div');
        div.className = 'nfcs-pick' + (String(f.id) === String(selectedId) ? ' selected' : '');
        const hex = f.color_hex ? ('#' + String(f.color_hex).replace(/^#/, '')) : '#888';
        div.innerHTML = '<span class="nfcs-swatch" style="background:' + hex + '"></span>' + nfcsEscape(nfcsFilamentOptionText(f));
        div.addEventListener('click', function () {
            document.getElementById('nfcs-rebind-fil-select').value = f.id;
            document.getElementById('nfcs-rebind-fil-selected').textContent = 'Selected: ' + nfcsFilamentOptionText(f);
            nfcsRenderRebindFilamentPicker(document.getElementById('nfcs-rebind-fil-search').value);
        });
        box.appendChild(div);
    });
}

async function nfcsRebindSave() {
    const tagType = nfcsEditState.tagType;
    let entityId = 0;
    if (tagType === 'spool') {
        entityId = parseInt(document.getElementById('nfcs-rebind-sp-select').value, 10) || 0;
        if (!entityId) { nfcsToast('Choose a spool to bind.'); return; }
    } else if (tagType === 'filament') {
        entityId = parseInt(document.getElementById('nfcs-rebind-fil-select').value, 10) || 0;
        if (!entityId) { nfcsToast('Choose a filament to bind.'); return; }
    }
    await nfcsRebindRequest(entityId);
}

async function nfcsRebindUnbind() {
    if (!confirm('Remove the current binding from this tag?\n\nThe Spoolman record is not affected.')) return;
    await nfcsRebindRequest(0);
}

async function nfcsRebindRequest(entityId) {
    const tagId = nfcsEditState.tagId;
    if (!tagId) return;
    try {
        const res = await fetch('/api/nfc/tags/' + encodeURIComponent(tagId) + '/rebind', {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ entity_id: entityId })
        });
        const data = await res.json().catch(function () { return {}; });
        if (!res.ok) { nfcsToast('Rebind failed: ' + (data.error || res.statusText)); return; }

        // Collapse the picker.
        document.getElementById('nfcs-rebind-spool-picker').style.display = 'none';
        document.getElementById('nfcs-rebind-filament-picker').style.display = 'none';

        // Update in-modal binding display.
        let newDesc = '— unbound —';
        if (entityId) {
            const entity = nfcsRebindPickerData.find(function (e) { return e.id === entityId; });
            if (entity) {
                newDesc = (nfcsEditState.tagType === 'spool') ? nfcsSpoolOptionText(entity) : nfcsFilamentOptionText(entity);
            } else {
                newDesc = (nfcsEditState.tagType === 'spool') ? 'Spool #' + entityId : 'Filament #' + entityId;
            }
        }
        document.getElementById('nfcs-rebind-current').textContent = newDesc;
        document.getElementById('nfcs-rebind-unbind-btn').style.display = entityId ? '' : 'none';
        nfcsEditState.boundEntityId = entityId || null;
        nfcsEditState.boundDesc = newDesc;

        nfcsToast(entityId === 0 ? 'Tag unbound.' : 'Binding updated.');
        nfcsReloadActive();
    } catch (e) {
        nfcsToast('Rebind error: ' + e.message);
    }
}

async function nfcsDeleteTag(tagId) {
    if (!confirm('Delete this NFC tag from the registry?\n\nThe bound Spoolman record is NOT affected.')) return;
    try {
        const res = await fetch('/api/nfc/tags/' + encodeURIComponent(tagId), { method: 'DELETE' });
        if (!res.ok) { const d = await res.json().catch(function () { return {}; }); nfcsToast(d.error || 'Delete failed'); return; }
        nfcsReloadActive();
    } catch (e) {
        nfcsToast('Delete error: ' + e.message);
    }
}

// Archive the bound Spoolman spool (zero remaining weight + move to Trash). Reuses the
// existing trash workflow; full tag archive/reuse semantics arrive in Stage 5.
async function nfcsArchiveSpool(spoolId) {
    if (!confirm('Archive Spoolman spool #' + spoolId + '?\n\nSets remaining weight to 0 and moves it to the Trash location.')) return;
    try {
        const res = await fetch('/api/nfc/spools/' + spoolId + '/trash', { method: 'POST' });
        if (!res.ok) { const d = await res.json().catch(function () { return {}; }); nfcsToast(d.error || 'Archive failed'); return; }
        nfcsLoadSpoolTags();
    } catch (e) {
        nfcsToast('Archive error: ' + e.message);
    }
}

async function nfcsDeleteSelected(kind) {
    const ids = Array.from(document.querySelectorAll('#nfcs-' + kind + '-rows .nfcs-' + kind.slice(0, 3) + '-check:checked'))
        .map(function (cb) { return cb.dataset.tagId; });
    if (ids.length === 0) { nfcsToast('No tags selected.'); return; }
    if (!confirm('Delete ' + ids.length + ' tag(s) from the registry? Spoolman records are not affected.')) return;
    for (const id of ids) {
        try { await fetch('/api/nfc/tags/' + encodeURIComponent(id), { method: 'DELETE' }); } catch (e) { /* continue */ }
    }
    nfcsReloadActive();
}

// ─── Add spool tag ───────────────────────────────────────────────────────────

function nfcsSetSpoolAddMode(mode) {
    const link = mode === 'link';
    document.getElementById('nfcs-spool-link-section').style.display = link ? '' : 'none';
    document.getElementById('nfcs-spool-unbound-note').style.display = link ? 'none' : '';
    document.getElementById('nfcs-spmode-link').classList.toggle('active', link);
    document.getElementById('nfcs-spmode-unbound').classList.toggle('active', !link);
}

// Tokenized, case-insensitive search. Whitespace tokens AND-match; a fully quoted
// "phrase" matches as a contiguous substring. Empty query matches everything.
function nfcsMatchSearch(text, query) {
    text = String(text == null ? '' : text).toLowerCase();
    query = String(query == null ? '' : query).trim().toLowerCase();
    if (!query) return true;
    const m = query.match(/^"(.*)"$/);
    if (m) return text.includes(m[1].trim());
    return query.split(/\s+/).every(function (tok) { return text.includes(tok); });
}

let nfcsSpoolPickerData = [];

function nfcsSpoolOptionText(s) {
    const vendor = s.vendor ? (s.vendor + ' · ') : '';
    const wt = (s.remaining_weight != null) ? (' (' + Math.round(s.remaining_weight) + 'g)') : '';
    return '[' + s.id + '] ' + vendor + (s.name || 'Spool') + ' · ' + (s.material || '') + wt;
}

function nfcsRenderSpoolPicker(filter) {
    const box = document.getElementById('nfcs-sp-options');
    const selectedId = document.getElementById('nfcs-sp-link-select').value;
    box.innerHTML = '';
    const matches = nfcsSpoolPickerData.filter(function (s) {
        return nfcsMatchSearch(nfcsSpoolOptionText(s), filter);
    });
    if (matches.length === 0) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">No spools match.</div>';
        return;
    }
    matches.forEach(function (s) {
        const div = document.createElement('div');
        div.className = 'nfcs-pick' + (String(s.id) === String(selectedId) ? ' selected' : '');
        const hex = s.color_hex ? ('#' + String(s.color_hex).replace(/^#/, '')) : '#888';
        div.innerHTML = '<span class="nfcs-swatch" style="background:' + hex + '"></span>' + nfcsEscape(nfcsSpoolOptionText(s));
        div.addEventListener('click', function () { nfcsSelectSpool(s.id); });
        box.appendChild(div);
    });
}

function nfcsFilterSpoolPicker() {
    nfcsRenderSpoolPicker(document.getElementById('nfcs-sp-search').value);
}

function nfcsSelectSpool(id) {
    document.getElementById('nfcs-sp-link-select').value = id;
    const s = nfcsSpoolPickerData.find(function (x) { return String(x.id) === String(id); });
    document.getElementById('nfcs-sp-selected').textContent = s ? ('Selected: ' + nfcsSpoolOptionText(s)) : '';
    nfcsRenderSpoolPicker(document.getElementById('nfcs-sp-search').value);
}

async function nfcsOpenAddSpool() {
    nfcsSetSpoolAddMode('link');
    document.getElementById('nfcs-sp-label').value = '';
    document.getElementById('nfcs-sp-link-select').value = '';
    document.getElementById('nfcs-sp-selected').textContent = '';
    document.getElementById('nfcs-sp-search').value = '';
    document.getElementById('nfcs-add-spool-overlay').style.display = 'flex';

    const box = document.getElementById('nfcs-sp-options');
    box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">Loading…</div>';
    try {
        const res = await fetch('/api/nfc/spools');
        const spools = await res.json();
        nfcsSpoolPickerData = Array.isArray(spools) ? spools : [];
        nfcsRenderSpoolPicker('');
    } catch (e) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:#ef4444;">Failed to load spools</div>';
    }
}

async function nfcsSubmitAddSpool() {
    const linking = document.getElementById('nfcs-spool-link-section').style.display !== 'none';
    const body = { tag_type: 'spool' };
    const label = document.getElementById('nfcs-sp-label').value.trim();
    if (label) body.label = label;
    if (linking) {
        const sid = parseInt(document.getElementById('nfcs-sp-link-select').value, 10);
        if (!sid) { nfcsToast('Choose a Spoolman spool to link.'); return; }
        body.spool_id = sid;
    }
    try {
        const res = await fetch('/api/nfc/tags', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });
        const data = await res.json();
        if (!res.ok) { nfcsToast('Create failed: ' + (data.error || res.statusText)); return; }
        nfcsCloseModal('nfcs-add-spool-overlay');
        await nfcsLoadSpoolTags();
        if (data.tag && data.tag.tag_id) {
            nfcsRenderPayload(data.tag.tag_id, data.tag_url, data.qr_code_base64, data.note, nfcsTagSubjectCache[data.tag.tag_id] || '');
        }
    } catch (e) {
        nfcsToast('Create error: ' + e.message);
    }
}

// ─── Write to NFC (display only) ───────────────────────────────────────────────

async function nfcsShowPayload(tagId) {
    try {
        const res = await fetch('/api/nfc/tags/' + encodeURIComponent(tagId) + '/payload');
        const data = await res.json();
        if (!res.ok) { nfcsToast(data.error || 'Failed to load payload'); return; }
        // Use live editState when this tag is open in the Edit modal; else use list-render cache.
        const subject = (nfcsEditState.tagId === tagId) ? nfcsBuildPayloadSubject() : (nfcsTagSubjectCache[tagId] || '');
        nfcsRenderPayload(tagId, data.tag_url, data.qr_code_base64, data.note, subject);
    } catch (e) {
        nfcsToast('Payload error: ' + e.message);
    }
}

function nfcsBuildPayloadSubject() {
    const label = nfcsEditState.orig || '';
    const type = nfcsEditState.tagType;
    if (type === 'location') {
        const kind = nfcsEditState.locationKind || 'toolhead';
        const emoji = { toolhead: '🖨️', inventory: '📦', archive: '🗄️', trash: '🗑️' }[kind] || '📍';
        const kindName = { toolhead: 'Toolhead', inventory: 'Inventory', archive: 'Archive', trash: 'Trash' }[kind] || kind;
        const kindStr = emoji + ' ' + kindName;
        return label ? label + ' - ' + kindStr : kindStr;
    }
    const bound = nfcsEditState.boundDesc;
    if (bound && bound !== '— unbound —') {
        return label ? label + ' - ' + bound : bound;
    }
    return label;
}

function nfcsRenderPayload(tagId, url, qrBase64, note, subject) {
    nfcsCurrentPayloadTagId = tagId;
    const subjectEl = document.getElementById('nfcs-payload-subject');
    if (subjectEl) subjectEl.textContent = subject || '';
    document.getElementById('nfcs-payload-note').textContent = note || '';
    document.getElementById('nfcs-payload-url').textContent = url || '';
    const img = document.getElementById('nfcs-payload-qr');
    img.src = qrBase64 ? ('data:image/png;base64,' + qrBase64) : '';
    document.getElementById('nfcs-payload-overlay').style.display = 'flex';
}

// Redo/replace: re-fetch the same tag_id's payload (for re-writing a failed/replacement tag).
function nfcsRedoPayload() {
    if (nfcsCurrentPayloadTagId) nfcsShowPayload(nfcsCurrentPayloadTagId);
}

// ─── Location tags (Stage 4) ──────────────────────────────────────────────────

const nfcsLocKindLabel = {
    toolhead: '🖨️ Toolhead', inventory: '📦 Inventory',
    archive: '🗄️ Archive', trash: '🗑️ Trash'
};

async function nfcsLoadLocationTags() {
    if (!document.getElementById('nfcs-location-rows')) return;
    try {
        const res = await fetch('/api/nfc/tags?type=location');
        const tags = await res.json();
        nfcsLocationData = Array.isArray(tags) ? tags : [];
        nfcsRenderKind('location');
    } catch (e) {
        nfcsToast('Failed to load location tags: ' + e.message);
    }
}

function nfcsRenderLocationRows(data) {
    const tbody = document.getElementById('nfcs-location-rows');
    const empty = document.getElementById('nfcs-location-empty');
    if (!tbody) return;
    const sorted = nfcsSortData('location', data);
    tbody.innerHTML = '';
    if (sorted.length === 0) { empty.style.display = ''; return; }
    empty.style.display = 'none';
    sorted.forEach(function (t) {
        const tr = document.createElement('tr');
        tr.className = 'nfcs-row-clickable';
        const shortId = t.tag_id.slice(0, 8);
        const label = t.label ? nfcsEscape(t.label) : '<span style="color:var(--text-secondary);">—</span>';
        const kindKey = t.location && t.location.kind ? t.location.kind : '';
        const kindBadge = kindKey ? nfcsEscape(nfcsLocKindLabel[kindKey] || kindKey) : '<span style="color:var(--text-secondary);">—</span>';
        const locKindEmoji = { toolhead: '🖨️', inventory: '📦', archive: '🗄️', trash: '🗑️' }[kindKey] || '📍';
        const locKindName = { toolhead: 'Toolhead', inventory: 'Inventory', archive: 'Archive', trash: 'Trash' }[kindKey] || kindKey || '—';
        const locSubjectLbl = t.label || '';
        const locKindStr = locKindEmoji + ' ' + locKindName;
        nfcsTagSubjectCache[t.tag_id] = locSubjectLbl ? locSubjectLbl + ' - ' + locKindStr : locKindStr;
        tr.innerHTML =
            '<td><input type="checkbox" class="nfcs-loc-check" data-tag-id="' + nfcsEscape(t.tag_id) + '" onclick="event.stopPropagation()"></td>' +
            '<td class="nfcs-tagid" title="' + nfcsEscape(t.tag_id) + '">' + nfcsEscape(shortId) + '…</td>' +
            '<td>' + label + '</td>' +
            '<td>' + kindBadge + '</td>' +
            '<td style="white-space:nowrap;">' +
            '<button class="nfcs-rowbtn" style="background:#7c3aed;color:#fff;" onclick="event.stopPropagation(); nfcsShowPayload(\'' + nfcsEscape(t.tag_id) + '\')">Write</button>' +
            '<button class="nfcs-rowbtn" style="background:#ef4444;color:#fff;" onclick="event.stopPropagation(); nfcsDeleteTag(\'' + nfcsEscape(t.tag_id) + '\')">Delete</button>' +
            '</td>';
        tr.title = 'Click to edit';
        tr.addEventListener('click', function () {
            nfcsOpenEdit(t.tag_id, 'location', t.label || '', '', null, kindKey, t.location || null);
        });
        tbody.appendChild(tr);
    });
}

// nfcsPrinterList caches the printer list for the Add Location dialog.
let nfcsPrinterList = [];

async function nfcsOpenAddLocation() {
    document.getElementById('nfcs-loc-kind').value = 'toolhead';
    document.getElementById('nfcs-loc-label').value = '';
    // Explicitly show the toolhead section before the async fetch.
    document.getElementById('nfcs-loc-toolhead-section').style.display = '';
    document.getElementById('nfcs-add-location-overlay').style.display = 'flex';

    const printerSel = document.getElementById('nfcs-loc-printer');
    printerSel.innerHTML = '<option value="">Loading…</option>';
    try {
        const res = await fetch('/api/printers');
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        nfcsPrinterList = [];
        Object.entries(data.printers || {}).forEach(function ([id, p]) {
            if (p) nfcsPrinterList.push({ id: id, name: p.name, toolheads: p.toolheads || 1 });
        });
        nfcsPrinterList.sort(function (a, b) { return a.name.localeCompare(b.name); });
        printerSel.innerHTML = nfcsPrinterList.length
            ? nfcsPrinterList.map(function (p) { return '<option value="' + nfcsEscape(p.id) + '" data-name="' + nfcsEscape(p.name) + '" data-toolheads="' + p.toolheads + '">' + nfcsEscape(p.name) + '</option>'; }).join('')
            : '<option value="">No printers configured</option>';
        nfcsLocationPrinterChange();
    } catch (e) {
        printerSel.innerHTML = '<option value="">Failed to load: ' + nfcsEscape(e.message) + '</option>';
    }
    nfcsLocationKindChange();
}

function nfcsLocationKindChange() {
    const kind = document.getElementById('nfcs-loc-kind').value;
    document.getElementById('nfcs-loc-toolhead-section').style.display = kind === 'toolhead' ? '' : 'none';
    if (kind !== 'toolhead') document.getElementById('nfcs-loc-label').placeholder = 'e.g. Drybox Shelf A';
    else document.getElementById('nfcs-loc-label').placeholder = 'e.g. Core One L - T0 (auto-filled)';
}

function nfcsLocationPrinterChange() {
    const sel = document.getElementById('nfcs-loc-printer');
    const opt = sel.options[sel.selectedIndex];
    const toolheads = opt ? parseInt(opt.dataset.toolheads, 10) || 1 : 1;
    const idxSel = document.getElementById('nfcs-loc-toolhead-idx');
    idxSel.innerHTML = Array.from({ length: toolheads }, function (_, i) {
        return '<option value="' + i + '">T' + i + '</option>';
    }).join('');
    nfcsLocationToolheadChange();
}

function nfcsLocationToolheadChange() {
    const printerSel = document.getElementById('nfcs-loc-printer');
    const opt = printerSel.options[printerSel.selectedIndex];
    const printerName = opt ? opt.dataset.name : '';
    const idx = document.getElementById('nfcs-loc-toolhead-idx').value;
    if (printerName) {
        document.getElementById('nfcs-loc-label').value = printerName + ' - T' + idx;
    }
}

async function nfcsSubmitAddLocation() {
    const kind = document.getElementById('nfcs-loc-kind').value;
    const rawLabel = document.getElementById('nfcs-loc-label').value.trim();
    const body = { tag_type: 'location', location_kind: kind };
    if (rawLabel) body.label = rawLabel;
    try {
        const res = await fetch('/api/nfc/tags', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });
        const data = await res.json();
        if (!res.ok) { nfcsToast('Create failed: ' + (data.error || res.statusText)); return; }
        nfcsCloseModal('nfcs-add-location-overlay');
        await nfcsLoadLocationTags();
        if (data.tag && data.tag.tag_id) {
            nfcsRenderPayload(data.tag.tag_id, data.tag_url, data.qr_code_base64, data.note, nfcsTagSubjectCache[data.tag.tag_id] || '');
        }
    } catch (e) {
        nfcsToast('Create error: ' + e.message);
    }
}

// ─── OpenPrintTag lookup tab ──────────────────────────────────────────────────

let nfcsOPTDebounceTimer = null;
let nfcsOPTLastResults = [];      // cache last search results for re-render on selection
let nfcsOPTSelectedFilament = null; // the currently selected filament result (no variant yet)

// Maps source_type values to short badge labels shown in the dropdown.
const nfcsOPTSourceTypeBadge = {
    ofd_api: 'OFD',
    filament_db_api: 'local'
};

// Maps source_type values to placeholder text for the search input.
const nfcsOPTSearchPlaceholder = {
    ofd_api: 'e.g. Polymaker PLA Black',
    filament_db_api: 'e.g. PLA 1.75mm'
};

function nfcsLoadOPTSources() {
    fetch('/api/openprinttag/sources')
        .then(function(r) { return r.json(); })
        .then(function(sources) {
            const sel = document.getElementById('nfcs-opt-source-select');
            const searchInput = document.getElementById('nfcs-opt-search');
            const resultsBox = document.getElementById('nfcs-opt-results');
            sel.innerHTML = '';
            const enabled = (sources || []).filter(function(s) { return s.enabled; });
            if (enabled.length === 0) {
                sel.innerHTML = '<option value="">No sources enabled — configure in Settings → Open Print Tag</option>';
                document.getElementById('nfcs-mode-openprinttag').disabled = true;
                if (searchInput) {
                    searchInput.disabled = true;
                    searchInput.placeholder = 'No sources enabled';
                }
                if (resultsBox) {
                    resultsBox.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">No sources enabled. Go to Settings → Open Print Tag to enable a source.</div>';
                }
                return;
            }
            document.getElementById('nfcs-mode-openprinttag').disabled = false;
            if (searchInput) searchInput.disabled = false;
            enabled.forEach(function(s) {
                const opt = document.createElement('option');
                opt.value = s.id;
                opt.dataset.sourceType = s.source_type;
                const badge = nfcsOPTSourceTypeBadge[s.source_type] || s.source_type;
                opt.textContent = s.name + ' [' + badge + ']';
                sel.appendChild(opt);
            });
            // Set placeholder for initially-selected source
            nfcsOPTUpdateSearchPlaceholder();
        })
        .catch(function() {
            document.getElementById('nfcs-opt-source-select').innerHTML =
                '<option value="">Failed to load sources</option>';
        });
}

// Updates the search input placeholder based on the currently selected source type.
function nfcsOPTUpdateSearchPlaceholder() {
    const sel = document.getElementById('nfcs-opt-source-select');
    const searchInput = document.getElementById('nfcs-opt-search');
    if (!sel || !searchInput) return;
    const opt = sel.options[sel.selectedIndex];
    const sourceType = opt ? opt.dataset.sourceType : '';
    searchInput.placeholder = nfcsOPTSearchPlaceholder[sourceType] || 'e.g. Polymaker PLA Black';
}

function nfcsOPTSourceChanged() {
    document.getElementById('nfcs-opt-search').value = '';
    document.getElementById('nfcs-opt-results').innerHTML = '';
    document.getElementById('nfcs-opt-selected-ref').value = '';
    document.getElementById('nfcs-opt-selected-source-id').value = '';
    document.getElementById('nfcs-opt-selected-info').textContent = '';
    document.getElementById('nfcs-opt-match-prompt').style.display = 'none';
    document.getElementById('nfcs-opt-variant-section').style.display = 'none';
    document.getElementById('nfcs-opt-selected-variant').value = '';
    nfcsOPTLastResults = [];
    nfcsOPTSelectedFilament = null;
    nfcsOPTUpdateSearchPlaceholder();
}

function nfcsSearchOPTDebounced() {
    clearTimeout(nfcsOPTDebounceTimer);
    nfcsOPTDebounceTimer = setTimeout(nfcsSearchOPT, 400);
}

async function nfcsSearchOPT() {
    const q = document.getElementById('nfcs-opt-search').value.trim();
    const sourceId = document.getElementById('nfcs-opt-source-select').value;
    if (!q || !sourceId) { document.getElementById('nfcs-opt-results').innerHTML = ''; return; }

    const box = document.getElementById('nfcs-opt-results');
    box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">Searching…</div>';
    try {
        const res = await fetch('/api/openprinttag/search?source_id=' + encodeURIComponent(sourceId) + '&q=' + encodeURIComponent(q));
        const results = await res.json();
        if (!res.ok) {
            box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:#ef4444;">' + nfcsEscape(results.error || res.statusText) + '</div>';
            return;
        }
        nfcsOPTLastResults = Array.isArray(results) ? results : [];
        nfcsRenderOPTResults(nfcsOPTLastResults);
    } catch (e) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:#ef4444;">Search error: ' + nfcsEscape(e.message) + '</div>';
    }
}

function nfcsRenderOPTResults(results) {
    const box = document.getElementById('nfcs-opt-results');
    const selectedFilamentRef = nfcsOPTSelectedFilament ? nfcsOPTSelectedFilament.source_ref : '';
    box.innerHTML = '';
    if (!results || results.length === 0) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">No results.</div>';
        return;
    }
    results.forEach(function(r) {
        const div = document.createElement('div');
        div.className = 'nfcs-pick' + (r.source_ref === selectedFilamentRef ? ' selected' : '');
        // Show brand - material - filament name (no colour — colour is picked separately)
        const label = [r.brand, r.material, r.filament_name].filter(Boolean).join(' - ');
        div.innerHTML = nfcsEscape(label) +
            '<span style="font-size:0.78em;color:var(--text-secondary);margin-left:6px;">' + nfcsEscape(r.source_name) + '</span>';
        div.addEventListener('click', function() { nfcsSelectOPTResult(r); });
        box.appendChild(div);
    });
}

function nfcsSelectOPTResult(result) {
    // Store the filament-level ref (without variant).
    // The variant slug will be appended to this ref once the user picks a colour.
    nfcsOPTSelectedFilament = result;
    document.getElementById('nfcs-opt-selected-ref').value = result.source_ref;
    document.getElementById('nfcs-opt-selected-source-id').value = result.source_id;
    const label = [result.brand, result.material, result.filament_name].filter(Boolean).join(' - ');
    document.getElementById('nfcs-opt-selected-info').textContent = 'Selected: ' + label;

    // Reset variant + match state
    document.getElementById('nfcs-opt-selected-variant').value = '';
    document.getElementById('nfcs-opt-match-prompt').style.display = 'none';

    // Update selection highlight in results list
    nfcsRenderOPTResults(nfcsOPTLastResults);

    // Fetch colour variants for this filament
    nfcsOPTFetchVariants(result);
}

async function nfcsOPTFetchVariants(result) {
    const section = document.getElementById('nfcs-opt-variant-section');
    const variantBox = document.getElementById('nfcs-opt-variants');
    section.style.display = '';
    variantBox.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">Loading colours…</div>';
    document.getElementById('nfcs-opt-temps').textContent = '';

    try {
        const res = await fetch('/api/openprinttag/variants?source_id=' +
            encodeURIComponent(result.source_id) + '&ref=' + encodeURIComponent(result.source_ref));
        const data = await res.json();
        if (!res.ok) {
            variantBox.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:#ef4444;">' +
                nfcsEscape(data.error || res.statusText) + '</div>';
            return;
        }
        // Show temperature summary
        const tempsEl = document.getElementById('nfcs-opt-temps');
        const parts = [];
        if (data.min_print_temp || data.max_print_temp) {
            parts.push('Nozzle: ' + (data.min_print_temp || '?') + '–' + (data.max_print_temp || '?') + '°C');
        }
        if (data.min_bed_temp || data.max_bed_temp) {
            parts.push('Bed: ' + (data.min_bed_temp || '?') + '–' + (data.max_bed_temp || '?') + '°C');
        }
        if (data.density) parts.push('Density: ' + data.density + ' g/cm³');
        tempsEl.textContent = parts.join('  ·  ');

        nfcsOPTRenderVariants(data.variants || [], result.source_ref, result.source_id);
    } catch (e) {
        variantBox.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:#ef4444;">Error: ' + nfcsEscape(e.message) + '</div>';
    }
}

function nfcsOPTRenderVariants(variants, filamentRef, sourceId) {
    const box = document.getElementById('nfcs-opt-variants');
    const selectedVariant = document.getElementById('nfcs-opt-selected-variant').value;
    box.innerHTML = '';
    if (!variants || variants.length === 0) {
        box.innerHTML = '<div class="nfcs-pick" style="cursor:default;color:var(--text-secondary);">No colour variants found.</div>';
        return;
    }
    variants.forEach(function(v) {
        const div = document.createElement('div');
        div.className = 'nfcs-pick' + (v.slug === selectedVariant ? ' selected' : '');
        const hex = v.color_hex ? '#' + v.color_hex.replace(/^#/, '') : '#888';
        div.innerHTML = '<span class="nfcs-swatch" style="background:' + hex + '"></span>' + nfcsEscape(v.name);
        div.addEventListener('click', function() {
            nfcsOPTSelectVariant(v.slug, v.name, v.color_hex, filamentRef, sourceId, variants);
        });
        box.appendChild(div);
    });
}

function nfcsOPTSelectVariant(variantSlug, variantName, colorHex, filamentRef, sourceId, variants) {
    const fullRef = filamentRef + '/variants/' + variantSlug;
    document.getElementById('nfcs-opt-selected-ref').value = fullRef;
    document.getElementById('nfcs-opt-selected-source-id').value = sourceId;
    document.getElementById('nfcs-opt-selected-variant').value = variantSlug;

    // Update filament info line to include colour
    if (nfcsOPTSelectedFilament) {
        const label = [nfcsOPTSelectedFilament.brand, nfcsOPTSelectedFilament.material,
            nfcsOPTSelectedFilament.filament_name, variantName].filter(Boolean).join(' - ');
        document.getElementById('nfcs-opt-selected-info').textContent = 'Selected: ' + label;
    }

    // Re-render variant list to update highlight
    nfcsOPTRenderVariants(variants, filamentRef, sourceId);

    // Fuzzy match against Spoolman filaments now that we have brand + colour
    const matchResult = nfcsOPTSelectedFilament
        ? { brand: nfcsOPTSelectedFilament.brand, material: nfcsOPTSelectedFilament.material, color_name: variantName }
        : { brand: '', material: '', color_name: variantName };
    const match = nfcsOPTFuzzyMatch(matchResult);
    const prompt = document.getElementById('nfcs-opt-match-prompt');
    if (match) {
        document.getElementById('nfcs-opt-match-name').textContent =
            (match.vendor ? match.vendor.name + ' ' : '') + (match.name || '');
        document.getElementById('nfcs-opt-match-filament-id').value = match.id;
        const radio = document.querySelector('input[name="nfcs-opt-action"][value="update_existing"]');
        if (radio) radio.checked = true;
        prompt.style.display = '';
    } else {
        document.getElementById('nfcs-opt-match-filament-id').value = '';
        prompt.style.display = 'none';
        const radio = document.querySelector('input[name="nfcs-opt-action"][value="create_new"]');
        if (radio) radio.checked = true;
    }
}

// nfcsOPTFuzzyMatch looks for a Spoolman filament that matches the OPT result by
// vendor name and material/color substring. Returns the first match or null.
function nfcsOPTFuzzyMatch(result) {
    if (!nfcsFilamentPickerData || nfcsFilamentPickerData.length === 0) return null;
    const brand = (result.brand || '').toLowerCase();
    const material = (result.material || '').toLowerCase();
    const colorName = (result.color_name || '').toLowerCase();
    return nfcsFilamentPickerData.find(function(f) {
        const vendorName = (f.vendor && f.vendor.name ? f.vendor.name : '').toLowerCase();
        const fName = (f.name || '').toLowerCase();
        const fMaterial = (f.material || '').toLowerCase();
        const brandMatch = brand && vendorName.includes(brand);
        const materialMatch = material && (fMaterial.includes(material) || fName.includes(material));
        const colorMatch = colorName && fName.includes(colorName);
        return brandMatch && (materialMatch || colorMatch);
    }) || null;
}
