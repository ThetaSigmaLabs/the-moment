// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

// ─── Helpers ──────────────────────────────────────────────────────────────────

function escapeHtmlAttribute(value) {
    if (value == null) return '';
    const div = document.createElement('div');
    div.textContent = value;
    return div.innerHTML.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function escapeHtml(value) {
    if (value == null) return '';
    const div = document.createElement('div');
    div.textContent = String(value);
    return div.innerHTML;
}

// Tracks which printer is currently open in the edit modal (needed by Toolheads tab).
var _editPrinterCurrentId = null;

function switchEditPrinterTab(tabName) {
    ['details', 'toolheads'].forEach(function(t) {
        var el = document.getElementById('editPrinterTab-' + t);
        if (el) el.style.display = (t === tabName) ? 'block' : 'none';
    });
    document.querySelectorAll('#editPrinterTabBar .hm-tab').forEach(function(btn) {
        btn.classList.toggle('active', btn.dataset.tab === tabName);
    });
    if (tabName === 'toolheads') {
        loadEditPrinterToolheadsTab();
    }
}

function loadEditPrinterToolheadsTab() {
    var pid = _editPrinterCurrentId;
    var container = document.getElementById('editPrinterToolheadsTable');
    if (!pid || !container) return;
    container.innerHTML = '<p style="color:#777;">Loading…</p>';

    Promise.all([
        fetch('/api/printers').then(function(r) { return r.json(); }),
        fetch('/api/printers/' + pid + '/toolheads/locations').then(function(r) { return r.json(); }),
        fetch('/api/spoolman/locations').then(function(r) { return r.json(); })
    ]).then(function(results) {
        var printerData  = results[0].printers[pid];
        var locData      = results[1];
        var spoolmanData = results[2];

        if (!printerData) {
            container.innerHTML = '<p style="color:#f88;">Printer not found.</p>';
            return;
        }

        var link = document.getElementById('editPrinterSpoolmanLink');
        if (link && spoolmanData.spoolman_url) {
            link.href = spoolmanData.spoolman_url;
        }

        var toolheadCount = printerData.toolheads || 1;
        var toolheadNames = printerData.toolhead_names || {};
        var savedLocs     = locData.locations || {};
        var locationList  = spoolmanData.locations || [];

        var html = '<table style="width:100%;border-collapse:collapse;">' +
            '<thead><tr style="font-size:0.8em;color:#aaa;border-bottom:1px solid #333;">' +
            '<th style="padding:6px 8px;text-align:left;width:40px;">#</th>' +
            '<th style="padding:6px 8px;text-align:left;">Name</th>' +
            '<th style="padding:6px 8px;text-align:left;">Spoolman Location</th>' +
            '</tr></thead><tbody>';

        for (var i = 0; i < toolheadCount; i++) {
            var thName   = escapeHtmlAttribute(toolheadNames[i] || ('Toolhead ' + i));
            var savedLoc = savedLocs[i] || '';

            var opts = '<option value="">— None —</option>';
            locationList.forEach(function(locName) {
                var sel = (locName === savedLoc) ? ' selected' : '';
                opts += '<option value="' + escapeHtmlAttribute(locName) + '"' + sel + '>' + escapeHtml(locName) + '</option>';
            });

            html += '<tr style="border-bottom:1px solid #2a2a2a;">' +
                '<td style="padding:8px;color:#aaa;">' + i + '</td>' +
                '<td style="padding:8px;">' +
                  '<input type="text" id="th-name-edit-' + i + '" data-toolhead-id="' + i + '"' +
                  ' value="' + thName + '"' +
                  ' style="width:100%;padding:6px;border-radius:4px;border:1px solid #555;' +
                         'background:rgba(255,255,255,0.08);color:#fff;font-size:0.9em;">' +
                '</td>' +
                '<td style="padding:8px;">' +
                  '<select id="th-loc-' + i + '" data-toolhead-id="' + i + '"' +
                  ' style="width:100%;padding:6px;border-radius:4px;border:1px solid #555;' +
                         'background:#2a2a2a;color:#fff;font-size:0.9em;">' +
                  opts +
                  '</select>' +
                '</td>' +
                '</tr>';
        }
        html += '</tbody></table>';
        container.innerHTML = html;
    }).catch(function(err) {
        container.innerHTML = '<p style="color:#f88;">Error loading toolheads: ' + escapeHtml(err.message) + '</p>';
    });
}

function saveToolheadLocations() {
    var pid = _editPrinterCurrentId;
    if (!pid) return;

    var promises = [];

    document.querySelectorAll('[id^="th-name-edit-"]').forEach(function(inp) {
        var tid  = parseInt(inp.dataset.toolheadId);
        var name = inp.value.trim();
        if (name) {
            promises.push(
                fetch('/api/printers/' + pid + '/toolheads/' + tid, {
                    method: 'PUT',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({name: name})
                }).then(function(r) { return r.json(); }).then(function(d) {
                    if (d.error) throw new Error(d.error);
                })
            );
        }
    });

    document.querySelectorAll('[id^="th-loc-"]').forEach(function(sel) {
        var tid     = parseInt(sel.dataset.toolheadId);
        var locName = sel.value;
        promises.push(
            fetch('/api/printers/' + pid + '/toolheads/' + tid + '/location', {
                method: 'PUT',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({location_name: locName})
            }).then(function(r) { return r.json(); }).then(function(d) {
                if (d.error) throw new Error(d.error);
            })
        );
    });

    var btn = document.getElementById('saveToolheadLocationsBtn');
    if (btn) { btn.disabled = true; btn.textContent = 'Saving…'; }

    Promise.all(promises)
        .then(function() {
            if (btn) { btn.disabled = false; btn.textContent = 'Save'; }
            loadPrinters();
        })
        .catch(function(err) {
            if (btn) { btn.disabled = false; btn.textContent = 'Save'; }
            showToast('Error saving: ' + err.message);
        });
}

function formatBytes(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

// ─── Printer List ─────────────────────────────────────────────────────────────

function loadPrinters() {
    fetch('/api/printers')
        .then(response => response.json())
        .then(data => {
            const printerList = document.getElementById('printer-list');
            printerList.innerHTML = '';

            if (data.printers && Object.keys(data.printers).length > 0) {
                for (const [printerId, printer] of Object.entries(data.printers)) {
                    if (printerId === 'no_printers') continue;
                    const card = document.createElement('div');
                    card.className = 'printer-card';
                    card.id = 'printer-card-' + printerId;
                    card.innerHTML = printer.is_virtual
                        ? buildVirtualPrinterCard(printerId, printer)
                        : buildRealPrinterCard(printerId, printer);
                    printerList.appendChild(card);
                }
            } else {
                printerList.innerHTML =
                    '<div class="printer-card"><p>No printers configured yet.</p>' +
                    '<p style="color:#aaa;font-size:0.9em;">Click <strong>Add Printer</strong> for real hardware or ' +
                    '<strong>Add Virtual Test Printer</strong> to test without a printer.</p></div>';
            }
        })
        .catch(error => {
            console.error('Error loading printers:', error);
            document.getElementById('printer-list').innerHTML =
                '<div class="printer-card"><p>Error loading printers. Please refresh.</p></div>';
        });
}

// ─── Real Printer Card ────────────────────────────────────────────────────────

function buildRealPrinterCard(printerId, printer) {
    const toolheadNames = printer.toolhead_names || {};
    let thHTML = '';
    for (let i = 0; i < (printer.toolheads || 1); i++) {
        const n = escapeHtmlAttribute(toolheadNames[i] || 'Toolhead ' + i);
        thHTML += '<div class="form-row" style="margin-bottom:10px;">' +
            '<label style="min-width:120px;">Toolhead ' + i + ':</label>' +
            '<input type="text" id="toolhead-name-' + printerId + '-' + i + '" value="' + n + '" ' +
            'class="toolhead-name-input" data-printer-id="' + printerId + '" data-toolhead-id="' + i + '" ' +
            'style="flex:1;padding:8px;border-radius:4px;border:1px solid #666;background:rgba(255,255,255,0.1);color:#fff;"></div>';
    }
    var isOctoPrint = (printer.printer_type === 'octoprint');
    var typeBadge = isOctoPrint
        ? '<span style="background:#3a4f6b;color:#90caf9;padding:2px 8px;border-radius:12px;font-size:0.75em;font-weight:600;margin-left:8px;">OctoPrint</span>'
        : '<span style="background:#3a5a3a;color:#a5d6a7;padding:2px 8px;border-radius:12px;font-size:0.75em;font-weight:600;margin-left:8px;">PrusaLink</span>';
    var apiKeyLine = isOctoPrint
        ? (printer.api_key ? '<div><strong>API Key:</strong> ••••••••</div>' : '')
        : '<div><strong>API Key:</strong> ' + (printer.api_key ? '••••••••' : 'Not configured') + '</div>';
    var octoPrintHint = isOctoPrint
        ? '<div style="color:#90caf9;font-size:0.85em;margin-top:4px;">Receives data via push from OctoPrint plugin</div>'
        : '';
    return '<div style="display:flex;align-items:center;gap:4px;margin-bottom:4px;">' +
        '<h3 style="margin:0;">' + escapeHtml(printer.name || 'Unknown') + '</h3>' + typeBadge + '</div>' +
        '<div class="printer-info">' +
        '<div><strong>Model:</strong> ' + escapeHtml(printer.model || 'Unknown') +
        ' (' + (printer.toolheads || 1) + ' toolhead' + (printer.toolheads > 1 ? 's' : '') + ')</div>' +
        '<div><strong>Address:</strong> ' + escapeHtml(printer.ip_address || 'Not configured') + '</div>' +
        apiKeyLine + octoPrintHint +
        '</div>' +
        '<div class="printer-actions">' +
        '<button class="btn btn-small" onclick="editPrinter(\'' + printerId + '\')">✏️ Edit</button>' +
        '<button class="btn btn-small" onclick="toggleToolheadNames(\'' + printerId + '\')">🔤 Rename Toolheads</button>' +
        '<button class="btn btn-small btn-secondary" ' +
            'id="debug-log-btn-' + printerId + '" ' +
            (printer.debug_log ? 'style="background:#7a5c1e;color:#ffd070;" ' : '') +
            'onclick="togglePrinterDebugLog(\'' + printerId + '\')" ' +
            'title="When on, subsequent prints record a poll transcript viewable in Print History">' +
            (printer.debug_log ? '🐛 Debug: ON' : '🐛 Debug: OFF') + '</button>' +
        '<button class="btn btn-small btn-secondary" onclick="openCommLog(\'' + printerId + '\',\'' + escapeHtmlAttribute(printer.name || '') + '\')" title="View live communication log">📡 Comm Log</button>' +
        '<button class="btn btn-small btn-danger" onclick="deletePrinter(\'' + printerId + '\')">🗑️ Delete</button>' +
        '</div>' +
        '<div id="toolhead-names-' + printerId + '" class="toolhead-names-section" style="display:none;margin-top:15px;padding:15px;background:rgba(255,255,255,0.05);border-radius:5px;">' +
        '<h4 style="margin-top:0;margin-bottom:15px;">Toolhead Names</h4>' + thHTML +
        '<div style="margin-top:15px;text-align:right;">' +
        '<button class="btn btn-small" onclick="saveToolheadNames(\'' + printerId + '\')">💾 Save Names</button>' +
        '<button class="btn btn-small btn-secondary" onclick="cancelToolheadNames(\'' + printerId + '\')">❌ Cancel</button>' +
        '</div></div>';
}

// ─── Virtual Printer Card ─────────────────────────────────────────────────────

function buildVirtualPrinterCard(printerId, printer) {
    const files = printer.files || [];
    const toolheadNames = printer.toolhead_names || {};

    let thHTML = '';
    for (let i = 0; i < (printer.toolheads || 1); i++) {
        const n = escapeHtmlAttribute(toolheadNames[i] || 'Toolhead ' + i);
        thHTML += '<div class="form-row" style="margin-bottom:10px;">' +
            '<label style="min-width:120px;">Toolhead ' + i + ':</label>' +
            '<input type="text" id="toolhead-name-' + printerId + '-' + i + '" value="' + n + '" ' +
            'class="toolhead-name-input" data-printer-id="' + printerId + '" data-toolhead-id="' + i + '" ' +
            'style="flex:1;padding:8px;border-radius:4px;border:1px solid #666;background:rgba(255,255,255,0.1);color:#fff;"></div>';
    }

    const fileRows = files.length > 0
        ? files.map(f => buildFileRow(printerId, f)).join('')
        : '<tr><td colspan="4" style="text-align:center;color:#888;padding:16px;">No files yet — upload a .gcode or .bgcode file.</td></tr>';

    return '<div style="display:flex;align-items:center;gap:10px;margin-bottom:4px;">' +
        '<h3 style="margin:0;">' + escapeHtml(printer.name || 'Virtual Printer') + '</h3>' +
        '<span style="background:#4a3f6b;color:#c8b8ff;padding:2px 8px;border-radius:12px;font-size:0.75em;font-weight:600;">🧪 VIRTUAL</span>' +
        '</div>' +
        '<div class="printer-info" style="margin-bottom:12px;">' +
        '<div><strong>Toolheads:</strong> ' + (printer.toolheads || 1) + '</div>' +
        '<div style="color:#aaa;font-size:0.85em;">Map spools on the Dashboard, then upload and process a G-code file here.</div>' +
        '</div>' +
        '<table style="width:100%;border-collapse:collapse;font-size:0.9em;margin-bottom:12px;">' +
        '<thead><tr style="border-bottom:1px solid #444;color:#aaa;">' +
        '<th style="text-align:left;padding:6px 8px;">File</th>' +
        '<th style="text-align:right;padding:6px 8px;">Size</th>' +
        '<th style="text-align:left;padding:6px 8px;">Uploaded</th>' +
        '<th style="text-align:right;padding:6px 8px;">Actions</th>' +
        '</tr></thead>' +
        '<tbody id="files-body-' + printerId + '">' + fileRows + '</tbody>' +
        '</table>' +
        '<div id="upload-area-' + printerId + '" ' +
        'style="border:2px dashed #555;border-radius:8px;padding:16px;text-align:center;cursor:pointer;transition:border-color 0.2s;" ' +
        'onclick="document.getElementById(\'file-input-' + printerId + '\').click()" ' +
        'ondragover="handleDragOver(event,\'' + printerId + '\')" ' +
        'ondragleave="handleDragLeave(event,\'' + printerId + '\')" ' +
        'ondrop="handleDrop(event,\'' + printerId + '\')">' +
        '<div style="font-size:1.5em;margin-bottom:4px;">📂</div>' +
        '<div style="color:#aaa;font-size:0.85em;">Click to upload or drag &amp; drop<br>' +
        '<span style="color:#777;font-size:0.9em;">.gcode or .bgcode</span></div>' +
        '<input type="file" id="file-input-' + printerId + '" accept=".gcode,.bgcode" style="display:none" ' +
        'onchange="handleFileSelected(event,\'' + printerId + '\')"></div>' +
        '<div id="upload-progress-' + printerId + '" style="display:none;margin-top:8px;">' +
        '<div style="background:#333;border-radius:4px;height:6px;overflow:hidden;">' +
        '<div id="upload-bar-' + printerId + '" style="background:#7c5cfc;height:100%;width:0%;transition:width 0.3s;"></div></div>' +
        '<div id="upload-status-' + printerId + '" style="color:#aaa;font-size:0.8em;margin-top:4px;text-align:center;">Uploading…</div>' +
        '</div>' +
        '<div class="printer-actions" style="margin-top:14px;">' +
        '<button class="btn btn-small" onclick="editVirtualPrinter(\'' + printerId + '\')">✏️ Edit</button>' +
        '<button class="btn btn-small" onclick="toggleToolheadNames(\'' + printerId + '\')">🔤 Rename Toolheads</button>' +
        '<button class="btn btn-small" onclick="exportVirtualPrinter(\'' + printerId + '\',\'' + escapeHtmlAttribute(printer.name) + '\')" title="Download complete printer snapshot as JSON">📤 Export</button>' +
        '<button class="btn btn-small btn-secondary" onclick="openCommLog(\'' + printerId + '\',\'' + escapeHtmlAttribute(printer.name || '') + '\')" title="View live communication log">📡 Comm Log</button>' +
        '<button class="btn btn-small btn-danger" onclick="deleteVirtualPrinter(\'' + printerId + '\',\'' + escapeHtmlAttribute(printer.name) + '\')">🗑️ Delete Printer</button>' +
        '</div>' +
        '<div id="toolhead-names-' + printerId + '" class="toolhead-names-section" style="display:none;margin-top:15px;padding:15px;background:rgba(255,255,255,0.05);border-radius:5px;">' +
        '<h4 style="margin-top:0;margin-bottom:15px;">Toolhead Names</h4>' + thHTML +
        '<div style="margin-top:15px;text-align:right;">' +
        '<button class="btn btn-small" onclick="saveToolheadNames(\'' + printerId + '\')">💾 Save Names</button>' +
        '<button class="btn btn-small btn-secondary" onclick="cancelToolheadNames(\'' + printerId + '\')">❌ Cancel</button>' +
        '</div></div>';
}

function buildFileRow(printerId, f) {
    const uploaded = new Date(f.uploaded_at).toLocaleDateString();
    const safeName = escapeHtml(f.display_name || f.filename);
    const safeAttr = escapeHtmlAttribute(f.display_name || f.filename);
    return '<tr id="file-row-' + f.id + '" style="border-bottom:1px solid #333;">' +
        '<td style="padding:8px;word-break:break-all;" title="' + safeAttr + '">' + safeName + '</td>' +
        '<td style="padding:8px;text-align:right;color:#aaa;white-space:nowrap;">' + formatBytes(f.file_size || 0) + '</td>' +
        '<td style="padding:8px;color:#aaa;white-space:nowrap;">' + uploaded + '</td>' +
        '<td style="padding:8px;text-align:right;white-space:nowrap;">' +
        '<button class="btn btn-small" onclick="processFile(\'' + printerId + '\',' + f.id + ',\'' + safeAttr + '\')" title="Parse and update Spoolman">▶ Process</button> ' +
        '<button class="btn btn-small btn-secondary" onclick="downloadFile(\'' + printerId + '\',' + f.id + ',\'' + safeAttr + '\')" title="Download">⬇</button> ' +
        '<button class="btn btn-small btn-danger" onclick="deleteFile(\'' + printerId + '\',' + f.id + ',\'' + safeAttr + '\')" title="Delete">🗑</button>' +
        '</td></tr>';
}

// ─── Drag and Drop ────────────────────────────────────────────────────────────

function handleDragOver(event, printerId) {
    event.preventDefault();
    var a = document.getElementById('upload-area-' + printerId);
    if (a) a.style.borderColor = '#7c5cfc';
}

function handleDragLeave(event, printerId) {
    var a = document.getElementById('upload-area-' + printerId);
    if (a) a.style.borderColor = '#555';
}

function handleDrop(event, printerId) {
    event.preventDefault();
    var a = document.getElementById('upload-area-' + printerId);
    if (a) a.style.borderColor = '#555';
    var files = event.dataTransfer.files;
    if (files.length > 0) uploadFileForPrinter(printerId, files[0]);
}

function handleFileSelected(event, printerId) {
    var file = event.target.files[0];
    if (file) {
        uploadFileForPrinter(printerId, file);
        event.target.value = '';
    }
}

// ─── File Upload ──────────────────────────────────────────────────────────────

function uploadFileForPrinter(printerId, file) {
    var name = file.name.toLowerCase();
    if (!name.endsWith('.gcode') && !name.endsWith('.bgcode')) {
        showToast('Only .gcode and .bgcode files are supported.');
        return;
    }

    var progress = document.getElementById('upload-progress-' + printerId);
    var bar = document.getElementById('upload-bar-' + printerId);
    var status = document.getElementById('upload-status-' + printerId);
    var area = document.getElementById('upload-area-' + printerId);

    if (progress) progress.style.display = 'block';
    if (area) area.style.opacity = '0.5';
    if (bar) bar.style.width = '30%';
    if (status) status.textContent = 'Uploading ' + file.name + '…';

    var formData = new FormData();
    formData.append('file', file);

    fetch('/api/printers/' + printerId + '/files', { method: 'POST', body: formData })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (bar) bar.style.width = '100%';
            if (data.error) {
                if (status) status.textContent = '❌ ' + data.error;
                setTimeout(function() {
                    if (progress) progress.style.display = 'none';
                    if (area) { area.style.opacity = '1'; area.style.borderColor = '#555'; }
                    if (bar) bar.style.width = '0%';
                }, 3000);
                return;
            }
            if (status) {
                status.textContent = data.has_usage ? '✅ Uploaded successfully' : '⚠️ Uploaded — no filament usage metadata found';
                status.style.color = data.has_usage ? '#81c784' : '#ffb74d';
            }
            setTimeout(function() {
                if (progress) progress.style.display = 'none';
                if (area) { area.style.opacity = '1'; area.style.borderColor = '#555'; }
                if (bar) bar.style.width = '0%';
                if (status) { status.textContent = 'Uploading…'; status.style.color = ''; }
                refreshVirtualPrinterFiles(printerId);
            }, 1800);
        })
        .catch(function(err) {
            if (status) status.textContent = '❌ ' + err.message;
            if (bar) bar.style.width = '0%';
            setTimeout(function() {
                if (progress) progress.style.display = 'none';
                if (area) area.style.opacity = '1';
            }, 3000);
        });
}

function refreshVirtualPrinterFiles(printerId) {
    fetch('/api/printers/' + printerId + '/files')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var tbody = document.getElementById('files-body-' + printerId);
            if (!tbody) return;
            var files = data.files || [];
            tbody.innerHTML = files.length > 0
                ? files.map(function(f) { return buildFileRow(printerId, f); }).join('')
                : '<tr><td colspan="4" style="text-align:center;color:#888;padding:16px;">No files yet — upload a .gcode or .bgcode file.</td></tr>';
        })
        .catch(function(err) { console.error('Error refreshing files:', err); });
}

// ─── File Actions ─────────────────────────────────────────────────────────────

function processFile(printerId, fileId, filename) {
    var row = document.getElementById('file-row-' + fileId);
    var btn = row ? row.querySelector('button') : null;
    if (btn) { btn.disabled = true; btn.textContent = '⏳ Processing…'; }

    fetch('/api/printers/' + printerId + '/files/' + fileId + '/process', { method: 'POST' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (btn) { btn.disabled = false; btn.textContent = '▶ Process'; }
            showProcessResult(filename, data.error ? null : data, data.error || null);
        })
        .catch(function(err) {
            if (btn) { btn.disabled = false; btn.textContent = '▶ Process'; }
            showProcessResult(filename, null, err.message);
        });
}

function showProcessResult(filename, data, errorMsg) {
    var modal = document.getElementById('processResultModal');
    var body  = document.getElementById('processResultBody');
    var hdr   = modal ? modal.querySelector('h3') : null;

    if (!modal || !body) {
        showToast(errorMsg ? ('Processing failed: ' + errorMsg) :
            ('Done! Total: ' + (data && data.total_g ? data.total_g.toFixed(2) : '?') + 'g'));
        return;
    }

    if (errorMsg) {
        if (hdr) hdr.textContent = '❌ Processing Failed';
        body.innerHTML = '<p><strong>File:</strong> ' + escapeHtml(filename) + '</p>' +
            '<div style="background:rgba(244,67,54,0.1);border:1px solid #f44336;border-radius:6px;padding:12px;color:#ef9a9a;">' +
            escapeHtml(errorMsg) + '</div>' +
            '<p style="margin-top:12px;color:#aaa;font-size:0.85em;">' +
            'Ensure spools are mapped to toolheads on the Dashboard before processing.</p>';
    } else {
        var hasSkipped = data && data.skipped_toolheads && data.skipped_toolheads.length > 0;
        if (hdr) hdr.textContent = hasSkipped ? '⚠️ Processed with Warnings' : '✅ Processing Complete';
        var usage = data.usage || {};
        var rows = Object.keys(usage).sort(function(a,b){return a-b;}).map(function(t) {
            return '<tr><td style="padding:6px 8px;">Toolhead ' + t + '</td>' +
                '<td style="padding:6px 8px;text-align:right;">' + Number(usage[t]).toFixed(2) + ' g</td></tr>';
        }).join('');
        var skippedHTML = '';
        if (hasSkipped) {
            var skippedList = data.skipped_toolheads.join(', T');
            skippedHTML = '<div style="background:rgba(255,152,0,0.1);border:1px solid #ff9800;' +
                'border-radius:6px;padding:10px;color:#ffcc80;font-size:0.85em;margin-top:10px;">' +
                '<strong>⚠️ Toolhead(s) T' + skippedList + ' had filament usage but no spool was mapped.</strong><br>' +
                'Go to the Dashboard, assign a spool to those toolheads, then process this file again.' +
                '</div>';
        }
        body.innerHTML = '<p><strong>File:</strong> ' + escapeHtml(filename) + '</p>' +
            '<table style="width:100%;border-collapse:collapse;margin-bottom:12px;font-size:0.9em;">' +
            '<thead><tr style="border-bottom:1px solid #444;color:#aaa;">' +
            '<th style="text-align:left;padding:6px 8px;">Toolhead</th>' +
            '<th style="text-align:right;padding:6px 8px;">Filament Used</th></tr></thead>' +
            '<tbody>' + rows + '</tbody>' +
            '<tfoot><tr style="border-top:1px solid #444;font-weight:600;">' +
            '<td style="padding:8px;">Total</td>' +
            '<td style="padding:8px;text-align:right;">' + Number(data.total_g || 0).toFixed(2) + ' g</td></tr></tfoot>' +
            '</table>' +
            (hasSkipped ? skippedHTML :
                '<div style="background:rgba(129,199,132,0.1);border:1px solid #81c784;border-radius:6px;' +
                'padding:10px;color:#a5d6a7;font-size:0.85em;">' +
                'Spoolman has been updated. Spool remaining weights reflect this print.</div>');
    }
    // Wire cost calculator — pass total grams, print time, and first mapped spool ID
    if (!errorMsg && data) {
        var firstSpoolId = 0;
        if (window._lastProcessSpoolId) firstSpoolId = window._lastProcessSpoolId;
        // Store print time so the cost modal can pre-fill it
        if (data.print_time_min && data.print_time_min > 0) {
            window._lastGcodePrintTimeMin = data.print_time_min;
        }
        if (typeof afterProcessSuccess === 'function') {
            afterProcessSuccess(data.total_g || 0, firstSpoolId);
        }
    } else {
        // Hide cost section on error
        var cs = document.getElementById('processCostSection');
        if (cs) cs.style.display = 'none';
        var cb = document.getElementById('costToggleBtn');
        if (cb) cb.style.display = 'none';
    }
    modal.style.display = 'block';
}

function closeProcessResultModal() {
    var m = document.getElementById('processResultModal');
    if (m) m.style.display = 'none';
}

function deleteFile(printerId, fileId, filename) {
    if (!confirm('Delete "' + filename + '"?\nThis cannot be undone.')) return;
    fetch('/api/printers/' + printerId + '/files/' + fileId, { method: 'DELETE' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { showToast('Error: ' + data.error); return; }
            var row = document.getElementById('file-row-' + fileId);
            if (row) {
                row.style.transition = 'opacity 0.3s';
                row.style.opacity = '0';
                setTimeout(function() {
                    row.remove();
                    var tbody = document.getElementById('files-body-' + printerId);
                    if (tbody && tbody.querySelectorAll('tr').length === 0) {
                        tbody.innerHTML = '<tr><td colspan="4" style="text-align:center;color:#888;padding:16px;">No files yet.</td></tr>';
                    }
                }, 300);
            }
        })
        .catch(function(err) { showToast('Error: ' + err.message); });
}

function downloadFile(printerId, fileId, filename) {
    var a = document.createElement('a');
    a.href = '/api/printers/' + printerId + '/files/' + fileId + '/download';
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
}

// ─── Virtual Printer Modal ────────────────────────────────────────────────────

function showVirtualPrinterForm() {
    var m = document.getElementById('addVirtualPrinterModal');
    if (!m) { showToast('Modal not found — please refresh.'); return; }
    m.style.display = 'block';
    document.getElementById('addVirtualPrinterForm').reset();
}

function closeVirtualPrinterModal() {
    var m = document.getElementById('addVirtualPrinterModal');
    if (m) m.style.display = 'none';
}

function deleteVirtualPrinter(printerId, name) {
    if (!confirm('Delete virtual printer "' + name + '" and all its uploaded files?\nThis cannot be undone.')) return;
    fetch('/api/printers/' + printerId, { method: 'DELETE' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { showToast('Error: ' + data.error); return; }
            loadPrinters();
        })
        .catch(function(err) { showToast('Error: ' + err.message); });
}

// ─── Export / Import ──────────────────────────────────────────────────────────

function exportVirtualPrinter(printerId, name) {
    // Trigger a browser download of the export JSON
    var a = document.createElement('a');
    a.href = '/api/printers/' + printerId + '/export';
    a.download = 'virtual-printer-' + name.toLowerCase().replace(/\s+/g, '-') + '.json';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
}

function showImportPrinterForm() {
    var modal = document.getElementById('importVirtualPrinterModal');
    if (!modal) { showToast('Import modal not found — please refresh.'); return; }
    modal.style.display = 'block';
    var input = document.getElementById('importFileInput');
    if (input) input.value = '';
    var status = document.getElementById('importStatus');
    if (status) { status.textContent = ''; status.style.display = 'none'; }
}

function closeImportPrinterModal() {
    var m = document.getElementById('importVirtualPrinterModal');
    if (m) m.style.display = 'none';
}

document.addEventListener('DOMContentLoaded', function() {
    var importForm = document.getElementById('importVirtualPrinterForm');
    if (!importForm) return;
    importForm.addEventListener('submit', function(e) {
        e.preventDefault();
        var fileInput = document.getElementById('importFileInput');
        if (!fileInput || !fileInput.files.length) {
            showToast('Please select a .json export file.');
            return;
        }
        var file = fileInput.files[0];
        if (!file.name.toLowerCase().endsWith('.json')) {
            showToast('Only .json export files are supported.');
            return;
        }

        var btn = importForm.querySelector('button[type="submit"]');
        var orig = btn ? btn.textContent : '';
        if (btn) { btn.disabled = true; btn.textContent = 'Importing…'; }

        var formData = new FormData();
        formData.append('file', file);

        var status = document.getElementById('importStatus');

        fetch('/api/printers/import', { method: 'POST', body: formData })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (btn) { btn.disabled = false; btn.textContent = orig; }
                if (data.error) {
                    if (status) {
                        status.textContent = '❌ ' + data.error;
                        status.style.color = '#ef9a9a';
                        status.style.display = 'block';
                    }
                    return;
                }
                closeImportPrinterModal();
                loadPrinters();
                // Switch to the printers settings tab so user sees the result
                var tab = document.querySelector('[onclick*="printers-tab"]') ||
                          document.querySelector('button[onclick*="printers"]');
                if (tab) tab.click();

                var msg = '✅ Imported "' + data.printer_name + '"';
                if (data.files_restored > 0) {
                    msg += ' with ' + data.files_restored + ' file(s).';
                }
                if (data.files_skipped > 0) {
                    msg += ' ' + data.files_skipped + ' file(s) could not be restored.';
                }
                msg += '\n\n' + data.spool_mappings_note;
                showToast(msg);
            })
            .catch(function(err) {
                if (btn) { btn.disabled = false; btn.textContent = orig; }
                if (status) {
                    status.textContent = '❌ ' + err.message;
                    status.style.color = '#ef9a9a';
                    status.style.display = 'block';
                }
            });
    });
});

document.addEventListener('DOMContentLoaded', function() {
    var form = document.getElementById('addVirtualPrinterForm');
    if (!form) return;
    form.addEventListener('submit', function(e) {
        e.preventDefault();
        var name = (document.getElementById('virtualPrinterName').value || '').trim();
        var toolheads = parseInt(document.getElementById('virtualPrinterToolheads').value) || 1;
        if (!name) { showToast('Printer name is required.'); return; }
        var btn = form.querySelector('button[type="submit"]');
        var orig = btn ? btn.textContent : '';
        if (btn) { btn.disabled = true; btn.textContent = 'Creating…'; }

        fetch('/api/printers/virtual', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: name, toolheads: toolheads })
        })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (btn) { btn.disabled = false; btn.textContent = orig; }
                if (data.error) { showToast('Error: ' + data.error); return; }
                closeVirtualPrinterModal();
                loadPrinters();
            })
            .catch(function(err) {
                if (btn) { btn.disabled = false; btn.textContent = orig; }
                showToast('Error: ' + err.message);
            });
    });
});

// ─── Real Printer Modals ──────────────────────────────────────────────────────

function onPrinterTypeChange(type, prefix) {
    var label     = document.getElementById(prefix + 'APIKeyLabel');
    var hint      = document.getElementById(prefix + 'APIKeyHint');
    var modelHint = document.getElementById(prefix + 'ModelHint');
    var ipHint    = document.getElementById(prefix + 'IPHint');
    var thLabel   = document.getElementById(prefix + 'ToolheadsLabel');
    var thHint    = document.getElementById(prefix + 'ToolheadsHint');
    if (type === 'octoprint') {
        if (label)     label.textContent     = 'API Key (optional)';
        if (hint)      hint.textContent      = 'Leave blank if your OctoPrint does not require an API key';
        if (modelHint) modelHint.textContent = 'Informational only — not used for OctoPrint';
        if (ipHint)    ipHint.textContent    = 'Hostname or IP address of your OctoPrint server';
        if (thLabel)   thLabel.textContent   = 'Number of Toolheads';
        if (thHint)    thHint.textContent    = 'How many toolheads does your printer have?';
    } else {
        if (label)     label.textContent     = 'API Key';
        if (hint)      hint.textContent      = 'Found in PrusaLink settings on your printer';
        if (modelHint) modelHint.textContent = 'Select your printer model (auto-detected for PrusaLink)';
        if (ipHint)    ipHint.textContent    = 'Hostname or IP address of your printer';
        if (thLabel)   thLabel.textContent   = 'Number of Toolheads';
        if (thHint)    thHint.textContent    = 'How many toolheads does your printer have?';
    }
}

function showAddPrinterForm() {
    document.getElementById('addPrinterModal').style.display = 'block';
    document.getElementById('addPrinterForm').reset();
    onPrinterTypeChange('prusalink', 'printer');
    setTimeout(function() {
        var b = document.querySelector('#addPrinterForm button[type="submit"]');
        if (b) { b.disabled = false; b.textContent = 'Add Printer'; }
    }, 0);
}

function closeAddPrinterModal() {
    document.getElementById('addPrinterModal').style.display = 'none';
    var b = document.querySelector('#addPrinterForm button[type="submit"]');
    if (b) { b.disabled = false; b.textContent = 'Add Printer'; }
}

function onProgressSnapshotModeChange(mode) {
    document.getElementById('progressSnapshotIntervalRow').style.display  = mode === 'interval'   ? '' : 'none';
    document.getElementById('progressSnapshotMilestonesRow').style.display = mode === 'milestones' ? '' : 'none';
}

function closeEditPrinterModal() {
    switchEditPrinterTab('details');
    document.getElementById('editPrinterModal').style.display = 'none';
    var b = document.querySelector('#editPrinterForm button[type="submit"]');
    if (b) { b.disabled = false; b.textContent = 'Update Printer'; }
    // Restore virtual-hidden fields
    document.getElementById('editPrinterIsVirtual').value = 'false';
    document.getElementById('editPrinterIP').required = true;
    ['editPrinterTypeGroup','editPrinterIPGroup','editPrinterAPIKeyGroup','editPrinterModelGroup'].forEach(function(id) {
        document.getElementById(id).style.display = '';
    });
    document.querySelector('#editPrinterModal .modal-header h3').textContent = 'Edit Printer';
    // Show tab bar for next open (in case it was hidden for a virtual printer)
    var tabBar = document.getElementById('editPrinterTabBar');
    if (tabBar) tabBar.style.display = '';
}

window.addEventListener('click', function(event) {
    var pairs = [
        ['addPrinterModal',        closeAddPrinterModal],
        ['editPrinterModal',       closeEditPrinterModal],
        ['addVirtualPrinterModal', closeVirtualPrinterModal],
        ['processResultModal',     closeProcessResultModal]
    ];
    for (var i = 0; i < pairs.length; i++) {
        var el = document.getElementById(pairs[i][0]);
        if (el && event.target === el) { pairs[i][1](); break; }
    }
});

function addPrinter(printerConfig) {
    return fetch('/api/printers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(printerConfig)
    }).then(function(r) { return r.json(); }).then(function(d) {
        if (d.error) throw new Error(d.error);
        return d;
    });
}

document.getElementById('addPrinterForm').addEventListener('submit', function(e) {
    e.preventDefault();
    if (!this.checkValidity()) return;
    var fd = new FormData(this);
    var btn = this.querySelector('button[type="submit"]');
    var orig = btn ? btn.textContent : '';
    var printerType = fd.get('printer_type') || 'prusalink';
    var name = fd.get('name');
    var ip = fd.get('ip_address');
    var key = fd.get('api_key');
    var toolheads = parseInt(fd.get('toolheads'));
    var model = fd.get('model') || 'Other';

    if (printerType === 'octoprint') {
        if (btn) { btn.disabled = true; btn.textContent = 'Adding…'; }
        addPrinter({ name: name, model: model, ip_address: ip, api_key: key,
            toolheads: toolheads, printer_type: printerType })
            .then(function() { closeAddPrinterModal(); loadPrinters(); })
            .catch(function(err) {
                if (btn) { btn.disabled = false; btn.textContent = orig; }
                showToast('Error: ' + err.message);
            });
    } else {
        if (btn) { btn.disabled = true; btn.textContent = 'Detecting model…'; }
        detectModelAndAddPrinter(name, ip, key, toolheads, printerType, btn, orig);
    }
});

document.getElementById('editPrinterForm').addEventListener('submit', function(e) {
    e.preventDefault();
    var fd = new FormData(this);
    var pid = fd.get('printerId');
    if (!pid) { showToast('Printer ID missing.'); return; }
    var btn = this.querySelector('button[type="submit"]');
    var orig = btn ? (btn.textContent || 'Update Printer') : '';
    if (btn) { btn.disabled = true; btn.textContent = 'Updating…'; }

    var isVirtual = fd.get('is_virtual') === 'true';
    var sortOrder = parseInt(fd.get('sort_order') || '0', 10) || 0;
    var pscMode = document.getElementById('editProgressSnapshotMode').value || 'none';
    var progressSnapshotConfig = { mode: pscMode };
    if (pscMode === 'interval') {
        progressSnapshotConfig.interval = parseFloat(document.getElementById('editProgressSnapshotInterval').value) || 10;
    } else if (pscMode === 'milestones') {
        progressSnapshotConfig.milestones = Array.from(document.querySelectorAll('.snapshotMilestone:checked')).map(function(cb) { return parseFloat(cb.value); });
    }
    var payload = isVirtual
        ? { name: fd.get('name'), toolheads: parseInt(fd.get('toolheads')), is_virtual: true,
            ip_address: 'virtual', model: 'Virtual Test Printer', printer_type: 'prusalink',
            sort_order: sortOrder }
        : { name: fd.get('name'), model: fd.get('model'),
            ip_address: fd.get('ip_address'), api_key: fd.get('api_key'),
            toolheads: parseInt(fd.get('toolheads')),
            printer_type: fd.get('printer_type') || 'prusalink',
            camera_snapshot_url: fd.get('camera_snapshot_url') || '',
            progress_snapshot_config: progressSnapshotConfig,
            sort_order: sortOrder };
    fetch('/api/printers/' + pid, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
    }).then(function(r) { return r.json(); }).then(function(data) {
        if (data.error) throw new Error(data.error);
        closeEditPrinterModal();
        loadPrinters();
    }).catch(function(err) {
        if (btn) { btn.disabled = false; btn.textContent = orig; }
        showToast('Error: ' + err.message);
    });
});

function detectModelAndAddPrinter(name, ip, key, toolheads, printerType, btn, orig) {
    fetch('/api/detect_printer', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ip_address: ip, api_key: key })
    }).then(function(r) { return r.json(); }).then(function(data) {
        if (data.error) throw new Error(data.error);
        return addPrinter({ name: name, model: data.model || 'Unknown',
            ip_address: ip, api_key: key, toolheads: toolheads,
            printer_type: printerType || 'prusalink' });
    }).then(function() {
        closeAddPrinterModal();
        loadPrinters();
    }).catch(function(err) {
        if (btn) { btn.disabled = false; btn.textContent = orig; }
        showToast('Error: ' + err.message);
    });
}

function editPrinter(printerId) {
    fetch('/api/printers').then(function(r) { return r.json(); }).then(function(data) {
        var p = data.printers[printerId];
        if (!p) { showToast('Printer not found'); return; }
        _editPrinterCurrentId = printerId;
        document.getElementById('editPrinterId').value = printerId;
        document.getElementById('editPrinterName').value = p.name || '';
        document.getElementById('editPrinterModel').value = p.model || '';
        document.getElementById('editPrinterIP').value = p.ip_address || '';
        document.getElementById('editPrinterAPIKey').value = p.api_key || '';
        document.getElementById('editPrinterToolheads').value = p.toolheads || 1;
        document.getElementById('editPrinterCameraURL').value = p.camera_snapshot_url || '';
        document.getElementById('editPrinterSortOrder').value = p.sort_order != null ? p.sort_order : 0;
        // Populate progress snapshot config
        var psc = p.progress_snapshot_config || {};
        var pscMode = psc.mode || 'none';
        document.getElementById('editProgressSnapshotMode').value = pscMode;
        onProgressSnapshotModeChange(pscMode);
        document.getElementById('editProgressSnapshotInterval').value = psc.interval || 10;
        var milestones = psc.milestones || [];
        document.querySelectorAll('.snapshotMilestone').forEach(function(cb) {
            cb.checked = milestones.indexOf(parseFloat(cb.value)) !== -1;
        });
        // Reset camera test result from previous session
        var testResult = document.getElementById('cameraTestResult');
        if (testResult) { testResult.style.display = 'none'; }
        var typeEl = document.getElementById('editPrinterType');
        var printerType = p.printer_type || 'prusalink';
        if (typeEl) { typeEl.value = printerType; }
        onPrinterTypeChange(printerType, 'editPrinter');
        var tabBar = document.getElementById('editPrinterTabBar');
        if (tabBar) tabBar.style.display = '';
        switchEditPrinterTab('details');
        document.getElementById('editPrinterModal').style.display = 'block';
    }).catch(function() { showToast('Error loading printer data', 'error'); });
}

function editVirtualPrinter(printerId) {
    fetch('/api/printers').then(function(r) { return r.json(); }).then(function(data) {
        var p = data.printers[printerId];
        if (!p) { showToast('Printer not found'); return; }
        _editPrinterCurrentId = printerId;
        document.getElementById('editPrinterId').value = printerId;
        document.getElementById('editPrinterIsVirtual').value = 'true';
        document.getElementById('editPrinterName').value = p.name || '';
        document.getElementById('editPrinterToolheads').value = p.toolheads || 1;
        // Hide fields that don't apply to virtual printers
        document.getElementById('editPrinterIP').required = false;
        ['editPrinterTypeGroup','editPrinterIPGroup','editPrinterAPIKeyGroup','editPrinterModelGroup'].forEach(function(id) {
            document.getElementById(id).style.display = 'none';
        });
        // Virtual printers have no physical toolheads — hide the Toolheads tab
        var tabBar = document.getElementById('editPrinterTabBar');
        if (tabBar) tabBar.style.display = 'none';
        switchEditPrinterTab('details');
        document.querySelector('#editPrinterModal .modal-header h3').textContent = 'Edit Virtual Printer';
        document.getElementById('editPrinterModal').style.display = 'block';
    }).catch(function() { showToast('Error loading printer data', 'error'); });
}

function deletePrinter(printerId) {
    if (!confirm('Delete this printer?')) return;
    fetch('/api/printers/' + printerId, { method: 'DELETE' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { showToast('Error: ' + data.error); return; }
            loadPrinters();
        })
        .catch(function(err) { showToast('Error: ' + err.message); });
}

function togglePrinterDebugLog(printerId) {
    var btn = document.getElementById('debug-log-btn-' + printerId);
    var currentlyOn = btn && btn.textContent.indexOf('ON') !== -1;
    var enable = !currentlyOn;
    fetch('/api/printers/' + printerId + '/debug-log', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: enable })
    }).then(function(r) { return r.json(); }).then(function(data) {
        if (data.error) { showToast('Error: ' + data.error); return; }
        if (btn) {
            btn.textContent = data.debug_log ? '🐛 Debug: ON' : '🐛 Debug: OFF';
            btn.style.background = data.debug_log ? '#7a5c1e' : '';
            btn.style.color = data.debug_log ? '#ffd070' : '';
        }
    }).catch(function(err) { showToast('Error: ' + err.message); });
}

// ─── Camera URL Test ─────────────────────────────────────────────────────────

function testCameraURL() {
    var url = (document.getElementById('editPrinterCameraURL').value || '').trim();
    if (!url) { showToast('Enter a camera URL first.'); return; }
    var btn = document.getElementById('testCameraBtn');
    var resultDiv = document.getElementById('cameraTestResult');
    var img = document.getElementById('cameraTestImg');
    var msg = document.getElementById('cameraTestMsg');
    if (btn) { btn.disabled = true; btn.textContent = 'Testing…'; }
    if (resultDiv) resultDiv.style.display = 'none';

    var printerId = document.getElementById('editPrinterId').value || '_';
    fetch('/api/printers/' + printerId + '/test-camera', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: url })
    }).then(function(r) { return r.json(); }).then(function(data) {
        if (btn) { btn.disabled = false; btn.textContent = 'Test'; }
        if (!resultDiv || !img || !msg) return;
        resultDiv.style.display = '';
        if (data.ok) {
            img.src = data.thumbnail;
            img.style.display = '';
            msg.textContent = 'Snapshot captured successfully.';
            msg.style.color = '#81c784';
        } else {
            img.style.display = 'none';
            msg.textContent = 'Failed: ' + (data.error || 'unknown error');
            msg.style.color = '#ef9a9a';
        }
    }).catch(function(err) {
        if (btn) { btn.disabled = false; btn.textContent = 'Test'; }
        showToast('Test error: ' + err.message);
    });
}

// ─── Toolhead Names ───────────────────────────────────────────────────────────

function toggleToolheadNames(printerId) {
    var s = document.getElementById('toolhead-names-' + printerId);
    if (!s) return;
    if (s.style.display === 'none') {
        s.style.display = 'block';
        s.querySelectorAll('.toolhead-name-input').forEach(function(i) {
            i.dataset.originalValue = i.value;
        });
    } else { s.style.display = 'none'; }
}

function saveToolheadNames(printerId) {
    var s = document.getElementById('toolhead-names-' + printerId);
    if (!s) return;
    var updates = [];
    s.querySelectorAll('.toolhead-name-input').forEach(function(inp) {
        var n = inp.value.trim();
        if (n && n !== (inp.dataset.originalValue || '')) {
            updates.push({ toolheadId: parseInt(inp.dataset.toolheadId), name: n });
        }
    });
    if (updates.length === 0) { showToast('No changes to save'); return; }
    Promise.all(updates.map(function(u) {
        return fetch('/api/printers/' + printerId + '/toolheads/' + u.toolheadId, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: u.name })
        }).then(function(r) { return r.json(); }).then(function(d) {
            if (d.error) throw new Error(d.error);
        });
    })).then(function() {
        s.style.display = 'none';
        loadPrinters();
    }).catch(function(err) { showToast('Error: ' + err.message); });
}

function cancelToolheadNames(printerId) {
    var s = document.getElementById('toolhead-names-' + printerId);
    if (!s) return;
    s.querySelectorAll('.toolhead-name-input').forEach(function(i) {
        if (i.dataset.originalValue) i.value = i.dataset.originalValue;
    });
    s.style.display = 'none';
}

// ─── Orphaned Spool Assignment Cleanup ───────────────────────────────────────

function checkOrphanedMappings() {
    var status = document.getElementById('orphanStatus');
    var clearBtn = document.getElementById('clearOrphansBtn');
    var checkBtn = document.getElementById('checkStuckBtn');

    if (checkBtn) { checkBtn.disabled = true; checkBtn.textContent = '🔍 Checking…'; }
    if (status) status.innerHTML = '<span style="color:#aaa;">Checking…</span>';

    function restoreCheckBtn() {
        if (checkBtn) { checkBtn.disabled = false; checkBtn.textContent = '🔍 Check for Stuck Assignments'; }
    }

    fetch('/api/orphaned-mappings')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            restoreCheckBtn();
            var orphans = data.orphans || [];
            if (orphans.length === 0) {
                if (status) status.innerHTML =
                    '<span style="color:#81c784;">✅ No stuck assignments found. All spools are correctly linked to active printers.</span>';
                if (clearBtn) clearBtn.style.display = 'none';
                return;
            }
            // Build a readable list
            var rows = orphans.map(function(o) {
                return '<tr>' +
                    '<td style="padding:4px 12px 4px 0;color:#ffb74d;">' + escapeHtml(o.printer_name) + '</td>' +
                    '<td style="padding:4px 12px 4px 0;color:#aaa;">T' + o.toolhead_id + '</td>' +
                    '<td style="padding:4px 0;">Spool #' + o.spool_id + '</td>' +
                    '</tr>';
            }).join('');
            if (status) status.innerHTML =
                '<p style="color:#ffb74d;margin:0 0 8px;">⚠️ Found ' + orphans.length +
                ' stuck assignment' + (orphans.length !== 1 ? 's' : '') +
                ' for printers that no longer exist:</p>' +
                '<table style="font-size:0.88em;border-collapse:collapse;">' + rows + '</table>';
            if (clearBtn) clearBtn.style.display = '';
        })
        .catch(function(err) {
            restoreCheckBtn();
            if (status) status.innerHTML =
                '<span style="color:#ef9a9a;">Error: ' + escapeHtml(err.message) + '</span>';
        });
}

function clearOrphanedMappings() {
    if (!confirm('Release all stuck spool assignments?\n\nThis will remove assignments for printers that no longer exist. Active printer assignments are not affected.')) return;

    var status = document.getElementById('orphanStatus');
    var clearBtn = document.getElementById('clearOrphansBtn');
    if (status) status.innerHTML = '<span style="color:#aaa;">Releasing…</span>';

    fetch('/api/orphaned-mappings', { method: 'DELETE' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) {
                if (status) status.innerHTML =
                    '<span style="color:#ef9a9a;">Error: ' + escapeHtml(data.error) + '</span>';
                return;
            }
            if (clearBtn) clearBtn.style.display = 'none';
            if (status) status.innerHTML =
                '<span style="color:#81c784;">✅ ' + data.message + '</span>';
            // Reload spools on the dashboard so they show as available
            if (typeof loadPrinters === 'function') loadPrinters();
        })
        .catch(function(err) {
            if (status) status.innerHTML =
                '<span style="color:#ef9a9a;">Error: ' + escapeHtml(err.message) + '</span>';
        });
}

