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
};

function nfcsEscape(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
        return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
    });
}

function nfcsToast(msg) {
    if (typeof showToast === 'function') showToast(msg); else alert(msg);
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
            const shortId = t.tag_id.slice(0, 8);
            let bound = '<span style="color:var(--text-secondary);">— unbound —</span>';
            if (t.filament) {
                const hex = t.filament.color_hex ? ('#' + String(t.filament.color_hex).replace(/^#/, '')) : '#888';
                const vend = t.filament.vendor ? (nfcsEscape(t.filament.vendor) + ' · ') : '';
                bound = '<span class="nfcs-swatch" style="background:' + hex + '"></span>' +
                    vend + nfcsEscape(t.filament.name || ('Filament #' + t.filament.id)) +
                    ' <span style="color:var(--text-secondary);">(' + nfcsEscape(t.filament.material || '') + ')</span>';
            } else if (t.bound_entity_id) {
                bound = '<span style="color:var(--text-secondary);">filament #' + t.bound_entity_id + ' (not in Spoolman)</span>';
            }
            const label = t.label ? nfcsEscape(t.label) : '<span style="color:var(--text-secondary);">—</span>';
            tr.innerHTML =
                '<td><input type="checkbox" class="nfcs-fil-check" data-tag-id="' + nfcsEscape(t.tag_id) + '"></td>' +
                '<td class="nfcs-tagid" title="' + nfcsEscape(t.tag_id) + '">' + nfcsEscape(shortId) + '…</td>' +
                '<td>' + label + '</td>' +
                '<td>' + bound + '</td>' +
                '<td style="white-space:nowrap;">' +
                '<button class="nfcs-rowbtn" style="background:#7c3aed;color:#fff;" onclick="nfcsShowPayload(\'' + nfcsEscape(t.tag_id) + '\')">Write</button>' +
                '<button class="nfcs-rowbtn" style="background:#374151;color:#fff;" onclick="nfcsEditLabel(\'' + nfcsEscape(t.tag_id) + '\', ' + JSON.stringify(t.label || '') + ')">Label</button>' +
                '<button class="nfcs-rowbtn" style="background:#ef4444;color:#fff;" onclick="nfcsDeleteTag(\'' + nfcsEscape(t.tag_id) + '\')">Delete</button>' +
                '</td>';
            tbody.appendChild(tr);
        });
    } catch (e) {
        nfcsToast('Failed to load filament tags: ' + e.message);
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

function nfcsSetAddMode(mode) {
    const link = mode === 'link';
    document.getElementById('nfcs-add-link-section').style.display = link ? '' : 'none';
    document.getElementById('nfcs-add-author-section').style.display = link ? 'none' : '';
    document.getElementById('nfcs-mode-link').classList.toggle('active', link);
    document.getElementById('nfcs-mode-author').classList.toggle('active', !link);
}

async function nfcsOpenAddFilament() {
    document.getElementById('nfcs-fil-label').value = '';
    ['manufacturer', 'material', 'colorname', 'colorhex', 'diameter', 'density', 'weight', 'price'].forEach(function (f) {
        const el = document.getElementById('nfcs-fil-' + f);
        if (el) el.value = '';
    });
    nfcsSetAddMode('link');
    document.getElementById('nfcs-add-filament-overlay').style.display = 'flex';

    const sel = document.getElementById('nfcs-fil-link-select');
    sel.innerHTML = '<option value="">Loading…</option>';
    try {
        const res = await fetch('/api/filaments');
        const filaments = await res.json();
        sel.innerHTML = '<option value="">— choose a filament —</option>';
        (filaments || []).forEach(function (f) {
            const vendor = f.vendor ? (f.vendor.name + ' · ') : '';
            const opt = document.createElement('option');
            opt.value = f.id;
            opt.textContent = '[' + f.id + '] ' + vendor + (f.name || 'Filament') + ' (' + (f.material || '') + ')';
            sel.appendChild(opt);
        });
    } catch (e) {
        sel.innerHTML = '<option value="">Failed to load filaments</option>';
    }
}

async function nfcsSubmitAddFilament() {
    const label = document.getElementById('nfcs-fil-label').value.trim();
    const authoring = document.getElementById('nfcs-add-author-section').style.display !== 'none';

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
    } else {
        const fid = parseInt(document.getElementById('nfcs-fil-link-select').value, 10);
        if (!fid) { nfcsToast('Choose a Spoolman filament to link.'); return; }
        body.filament_id = fid;
    }

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

// ─── Label / delete ────────────────────────────────────────────────────────────

async function nfcsEditLabel(tagId, current) {
    const next = prompt('Display label (nickname). Leave blank to clear.', current || '');
    if (next === null) return;
    try {
        const res = await fetch('/api/nfc/tags/' + encodeURIComponent(tagId) + '/label', {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ label: next.trim() })
        });
        const data = await res.json();
        if (!res.ok) { nfcsToast(data.error || 'Label update failed'); return; }
        await nfcsLoadFilamentTags();
    } catch (e) {
        nfcsToast('Label error: ' + e.message);
    }
}

async function nfcsDeleteTag(tagId) {
    if (!confirm('Delete this filament tag from the registry?\n\nThe Spoolman filament record is NOT affected.')) return;
    try {
        const res = await fetch('/api/nfc/tags/' + encodeURIComponent(tagId), { method: 'DELETE' });
        if (!res.ok) { const d = await res.json().catch(function () { return {}; }); nfcsToast(d.error || 'Delete failed'); return; }
        await nfcsLoadFilamentTags();
    } catch (e) {
        nfcsToast('Delete error: ' + e.message);
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
    await nfcsLoadFilamentTags();
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
