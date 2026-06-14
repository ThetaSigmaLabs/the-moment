// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u
//
// NFCs tab — tag registry management (Stage 2: Filament sub-tab).
// Binding lives in nfc_tags; Spoolman remains source of truth for filament data.

let nfcsCurrentPayloadTagId = null;

// Lazy-load hook called by switchNfcsSubTab when a sub-tab is shown.
window.nfcsOnSubTabShown = function (name) {
    if (name === 'filament') nfcsLoadFilamentTags();
    else if (name === 'spool') nfcsLoadSpoolTags();
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
    if (filament && filament.style.display !== 'none') nfcsLoadFilamentTags();
    else if (spool && spool.style.display !== 'none') nfcsLoadSpoolTags();
}

async function nfcsLoadFilamentTags() {
    const tbody = document.getElementById('nfcs-filament-rows');
    const empty = document.getElementById('nfcs-filament-empty');
    if (!tbody) return;
    try {
        const res = await fetch('/api/nfc/tags?type=filament');
        const tags = await res.json();
        tbody.innerHTML = '';
        if (!Array.isArray(tags) || tags.length === 0) {
            empty.style.display = '';
            return;
        }
        empty.style.display = 'none';
        tags.forEach(function (t) {
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
    } catch (e) {
        nfcsToast('Failed to load filament tags: ' + e.message);
    }
}

async function nfcsLoadSpoolTags() {
    const tbody = document.getElementById('nfcs-spool-rows');
    const empty = document.getElementById('nfcs-spool-empty');
    if (!tbody) return;
    const unboundOnly = document.getElementById('nfcs-spool-unbound-only') && document.getElementById('nfcs-spool-unbound-only').checked;
    try {
        const res = await fetch('/api/nfc/tags?type=spool');
        let tags = await res.json();
        if (!Array.isArray(tags)) tags = [];
        if (unboundOnly) tags = tags.filter(function (t) { return !t.bound_entity_id; });
        tbody.innerHTML = '';
        if (tags.length === 0) { empty.style.display = ''; return; }
        empty.style.display = 'none';
        tags.forEach(function (t) {
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
            const archived = t.spool && t.spool.archived;
            const statusTxt = archived ? 'spool archived' : t.status;
            const label = t.label ? nfcsEscape(t.label) : '<span style="color:var(--text-secondary);">—</span>';
            let actions =
                '<button class="nfcs-rowbtn" style="background:#7c3aed;color:#fff;" onclick="event.stopPropagation(); nfcsShowPayload(\'' + nfcsEscape(t.tag_id) + '\')">Write</button>';
            if (t.spool && !archived) {
                actions += '<button class="nfcs-rowbtn" style="background:#b45309;color:#fff;" onclick="event.stopPropagation(); nfcsArchiveSpool(' + t.spool.id + ')" title="Zero remaining weight and move the Spoolman spool to Trash">Archive spool</button>';
            }
            actions += '<button class="nfcs-rowbtn" style="background:#ef4444;color:#fff;" onclick="event.stopPropagation(); nfcsDeleteTag(\'' + nfcsEscape(t.tag_id) + '\')">Delete</button>';
            tr.innerHTML =
                '<td><input type="checkbox" class="nfcs-spo-check" data-tag-id="' + nfcsEscape(t.tag_id) + '" onclick="event.stopPropagation()"></td>' +
                '<td class="nfcs-tagid" title="' + nfcsEscape(t.tag_id) + '">' + nfcsEscape(shortId) + '…</td>' +
                '<td>' + label + '</td>' +
                '<td>' + bound + '</td>' +
                '<td>' + nfcsEscape(statusTxt) + '</td>' +
                '<td style="white-space:nowrap;">' + actions + '</td>';
            tr.title = 'Click to edit';
            tr.addEventListener('click', function () { nfcsOpenEdit(t.tag_id, 'spool', t.label || '', boundText, t.bound_entity_id); });
            tbody.appendChild(tr);
        });
    } catch (e) {
        nfcsToast('Failed to load spool tags: ' + e.message);
    }
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

// Three modes: link an existing Spoolman filament, author a new one, or create an unbound
// filament tag (a filament type that may not exist in Spoolman yet).
function nfcsSetAddMode(mode) {
    document.getElementById('nfcs-add-link-section').style.display = mode === 'link' ? '' : 'none';
    document.getElementById('nfcs-add-author-section').style.display = mode === 'author' ? '' : 'none';
    document.getElementById('nfcs-add-unbound-section').style.display = mode === 'unbound' ? '' : 'none';
    document.getElementById('nfcs-mode-link').classList.toggle('active', mode === 'link');
    document.getElementById('nfcs-mode-author').classList.toggle('active', mode === 'author');
    document.getElementById('nfcs-mode-unbound').classList.toggle('active', mode === 'unbound');
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
}

async function nfcsSubmitAddFilament() {
    const label = document.getElementById('nfcs-fil-label').value.trim();
    const authoring = document.getElementById('nfcs-add-author-section').style.display !== 'none';
    const linking = document.getElementById('nfcs-add-link-section').style.display !== 'none';

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
        // Immediately show the write-to-NFC payload for the new tag.
        if (data.tag && data.tag.tag_id) nfcsRenderPayload(data.tag.tag_id, data.tag_url, data.qr_code_base64, data.note);
    } catch (e) {
        nfcsToast('Create error: ' + e.message);
    }
}

// ─── Edit tag (inline label, blur-to-save, rebind) ────────────────────────────

// Tracks the tag being edited so blur-to-save, Write/Delete, and rebind act on it.
let nfcsEditState = { tagId: null, tagType: null, orig: '', boundEntityId: null };

// Open the Edit dialog. boundDesc: plain-text binding summary; boundEntityId: current id or null.
function nfcsOpenEdit(tagId, tagType, label, boundDesc, boundEntityId) {
    nfcsEditState = { tagId: tagId, tagType: tagType, orig: label || '', boundEntityId: boundEntityId || null };
    const titles = { spool: '🧵 Spool tag', filament: '🧪 Filament tag', location: '📍 Location tag' };
    document.getElementById('nfcs-edit-title').textContent = titles[tagType] || '🏷️ Tag';
    document.getElementById('nfcs-edit-tagid').textContent = tagId;
    document.getElementById('nfcs-edit-savestate').textContent = '';
    const input = document.getElementById('nfcs-edit-label');
    input.value = label || '';

    // Rebind section: show for spool/filament, hide for location (Stage 4).
    const rebindSection = document.getElementById('nfcs-rebind-section');
    if (rebindSection) {
        rebindSection.style.display = tagType === 'location' ? 'none' : '';
        document.getElementById('nfcs-rebind-current').textContent = boundDesc || '— unbound —';
        document.getElementById('nfcs-rebind-unbind-btn').style.display = boundEntityId ? '' : 'none';
        document.getElementById('nfcs-rebind-spool-picker').style.display = 'none';
        document.getElementById('nfcs-rebind-filament-picker').style.display = 'none';
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
        const newDesc = entityId === 0 ? '— unbound —'
            : (nfcsEditState.tagType === 'spool' ? 'Spool #' + entityId : 'Filament #' + entityId);
        document.getElementById('nfcs-rebind-current').textContent = newDesc;
        document.getElementById('nfcs-rebind-unbind-btn').style.display = entityId ? '' : 'none';
        nfcsEditState.boundEntityId = entityId || null;

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
        if (data.tag && data.tag.tag_id) nfcsRenderPayload(data.tag.tag_id, data.tag_url, data.qr_code_base64, data.note);
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
        nfcsRenderPayload(tagId, data.tag_url, data.qr_code_base64, data.note);
    } catch (e) {
        nfcsToast('Payload error: ' + e.message);
    }
}

function nfcsRenderPayload(tagId, url, qrBase64, note) {
    nfcsCurrentPayloadTagId = tagId;
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
