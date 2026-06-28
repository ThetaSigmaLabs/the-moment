// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

// The Moment Dashboard - Main JavaScript Functions

const VALID_TABS = ['dashboard', 'history', 'spools', 'filament', 'printers', 'nfcs', 'settings'];

// Tab switching functionality
function switchTab(tabName) {
    if (!VALID_TABS.includes(tabName)) tabName = 'dashboard';

    document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
    document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));

    document.getElementById(tabName + '-tab').classList.add('active');
    const btn = [...document.querySelectorAll('.tab')]
        .find(t => t.getAttribute('onclick')?.includes(`'${tabName}'`));
    if (btn) btn.classList.add('active');

    if (tabName === 'dashboard') {
        loadDashboardStats();
    }

    // Sync Spoolman locations when Spools tab is opened so changes made in
    // Spoolman are reflected immediately rather than waiting for the 5-min poll.
    if (tabName === 'spools') {
        fetch('/api/nfc/sync-locations-now', { method: 'POST' }).catch(() => {});
    }

    if (tabName === 'filament') {
        loadFilaments();
    }

    if (tabName === 'printers') {
        loadPrinters();
        loadLocationTags();
    }

    // Load configuration when settings tab is opened
    if (tabName === 'settings') {
        const activeTabContent = document.querySelector('.settings-tab-content.active');
        if (activeTabContent) {
            const tabId = activeTabContent.id.replace('-tab', '');
            if (tabId === 'basic-config') {
                loadConfiguration();
            } else if (tabId === 'cost') {
                loadCostSettings();
            } else if (tabId === 'advanced') {
                loadAdvancedSettings();
                loadAutoAssignSettings();
            }
        }
    }

    // When the NFCs tab is shown, load whichever sub-tab is currently visible.
    // switchNfcsSubTab only fires on explicit button clicks, so the initial/return
    // render would stay blank without this explicit trigger.
    if (tabName === 'nfcs' && typeof window.nfcsOnSubTabShown === 'function') {
        const activeSubTab = ['spool', 'location', 'filament'].find(function (n) {
            const pane = document.getElementById('nfcs-subtab-' + n);
            return pane && pane.style.display !== 'none';
        }) || 'spool';
        window.nfcsOnSubTabShown(activeSubTab);
    }

    if (location.hash !== '#' + tabName) {
        location.hash = tabName;
    }
}

function toggleConfig() {
    switchTab('settings');
}

function showAbout() {
    switchTab('settings');
    switchSettingsTab('about', null);
}

// Settings sub-tab switching functionality
function switchSettingsTab(tabName, clickedElement) {
    // Hide all settings tab contents
    document.querySelectorAll('.settings-tab-content').forEach(tab => {
        tab.classList.remove('active');
    });

    // Remove active class from all settings tabs
    document.querySelectorAll('.settings-tab').forEach(tab => {
        tab.classList.remove('active');
    });

    // Show selected tab content
    const targetTab = document.getElementById(tabName + '-tab');
    if (targetTab) {
        targetTab.classList.add('active');
    }

    // Add active class to clicked tab
    if (clickedElement) {
        clickedElement.classList.add('active');
    } else {
        // Fallback: find the tab button by onclick attribute
        const tabButtons = document.querySelectorAll('.settings-tab');
        tabButtons.forEach(btn => {
            if (btn.getAttribute('onclick') && btn.getAttribute('onclick').includes(tabName)) {
                btn.classList.add('active');
            }
        });
    }

    // Load data for specific tabs
    if (tabName === 'basic-config') {
        loadConfiguration();
    } else if (tabName === 'cost') {
        loadCostSettings();
    } else if (tabName === 'openprinttag') {
        loadOPTSettings();
    } else if (tabName === 'advanced') {
        loadAdvancedSettings();
        loadAutoAssignSettings();
        loadBackupList();
        loadBackupDiskSpace();
        checkRestorePending();
    }
}

// Configuration Management
function loadConfiguration() {
    fetch('/api/config')
        .then(response => response.json())
        .then(config => {
            const form = document.getElementById('config-form');
            form.innerHTML = `
                <div style="max-width: 600px; margin: 0 auto;">
                    <div class="form-group">
                        <label><strong>Spoolman URL (internal):</strong></label>
                        <input type="text" id="spoolman_url" value="${config.spoolman_url || ''}" placeholder="http://spoolman:8000">
                        <small>URL The Moment uses to call the Spoolman API. Use a hostname reachable from this container (e.g. <code>http://spoolman:8000</code> in Docker, <code>http://localhost:7912</code> on bare metal).</small>
                    </div>
                    <div class="form-group">
                        <label><strong>Spoolman URL (external / browser):</strong></label>
                        <input type="text" id="spoolman_external_url" value="${config.spoolman_external_url || ''}" placeholder="http://your-host:7912">
                        <small>URL the user's browser should use for "Open Spoolman" links. Leave blank to fall back to the internal URL above.</small>
                    </div>
                    <div class="form-group">
                        <label><strong>Poll Interval (seconds):</strong></label>
                        <input type="number" id="poll_interval" value="${config.poll_interval || '30'}" min="10" max="300">
                        <small>How often to check printer status</small>
                    </div>
                    <div style="margin-top: 20px; text-align: center; display: flex; gap: 10px; justify-content: center; align-items: center;">
                        <button class="btn btn-secondary" onclick="testSpoolmanURL()">🔌 Test URL</button>
                        <button class="btn" onclick="saveConfiguration()">💾 Save Configuration</button>
                        <span id="spoolman-test-result" style="font-size: 0.9em;"></span>
                    </div>
                </div>
            `;
        })
        .catch(error => {
            console.error('Error loading configuration:', error);
            document.getElementById('config-form').innerHTML = '<p style="color: red;">Error loading configuration</p>';
        });
}

function saveConfiguration() {
    const config = {
        spoolman_url: document.getElementById('spoolman_url').value,
        spoolman_external_url: document.getElementById('spoolman_external_url').value,
        poll_interval: document.getElementById('poll_interval').value
    };

    fetch('/api/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config)
    })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                showToast('Error saving configuration: ' + data.error);
            } else {
                showToast('Configuration saved successfully! The Moment will restart.', 'success');
                location.reload();
            }
        })
        .catch(error => {
            showToast('Error saving configuration: ' + error.message);
        });
}

function testSpoolmanURL() {
    const resultEl = document.getElementById('spoolman-test-result');
    const active = document.activeElement && document.activeElement.id;
    const urlEl = (active === 'spoolman_external_url')
        ? document.getElementById('spoolman_external_url')
        : document.getElementById('spoolman_url');
    const url = urlEl.value.trim();

    if (!url) {
        resultEl.style.color = '#f0a500';
        resultEl.textContent = '⚠️ Enter a URL first';
        return;
    }

    resultEl.style.color = '#aaa';
    resultEl.textContent = 'Testing…';

    fetch('/api/spoolman/test-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url })
    })
        .then(response => response.json())
        .then(data => {
            if (data.connected) {
                resultEl.style.color = '#4caf50';
                resultEl.textContent = '✅ Connected';
            } else {
                resultEl.style.color = '#f44336';
                resultEl.textContent = '❌ ' + (data.error || 'Connection failed');
            }
        })
        .catch(error => {
            resultEl.style.color = '#f44336';
            resultEl.textContent = '❌ ' + error.message;
        });
}

function saveAutoAssignSettings() {
    const enabled = document.getElementById('autoAssignPreviousSpoolEnabled').checked;

    fetch('/api/config/auto-assign-previous-spool', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled })
    })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                showToast('Error saving auto-assign settings: ' + data.error);
            } else {
                showToast('Auto-assign settings saved successfully!', 'success');
            }
        })
        .catch(error => {
            showToast('Error saving auto-assign settings: ' + error.message);
        });
}

// Cost settings management
function saveCostSettings() {
    const settings = {
        electricity_rate: parseFloat(document.getElementById('electricity_rate').value),
        printer_wattage: parseFloat(document.getElementById('printer_wattage').value),
        maintenance_rate: parseFloat(document.getElementById('maintenance_rate').value),
        currency: document.getElementById('currency').value,
        include_electricity: document.getElementById('include_electricity').checked,
        include_maintenance: document.getElementById('include_maintenance').checked
    };

    fetch('/api/config/cost-settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(settings)
    })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                showToast('Error saving cost settings: ' + data.error);
            } else {
                showToast('Cost settings saved successfully!', 'success');
                if (window.costCalculator) {
                    window.costCalculator.loadSettings();
                }
            }
        })
        .catch(error => {
            showToast('Error saving cost settings: ' + error.message);
        });
}

function loadCostSettings() {
    fetch('/api/config/cost-settings')
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                console.error('Error loading cost settings:', data.error);
                return;
            }

            document.getElementById('electricity_rate').value = data.electricity_rate || 0.12;
            document.getElementById('printer_wattage').value = data.printer_wattage || 250;
            document.getElementById('maintenance_rate').value = data.maintenance_rate || 0.50;
            document.getElementById('currency').value = data.currency || 'USD';
            document.getElementById('include_electricity').checked = data.include_electricity !== false;
            document.getElementById('include_maintenance').checked = data.include_maintenance !== false;
        })
        .catch(error => {
            console.error('Error loading cost settings:', error);
        });
}

// Advanced Settings Functions
function loadAdvancedSettings() {
    fetch('/api/config')
        .then(response => response.json())
        .then(config => {
            document.getElementById('prusalinkTimeout').value = config.prusalink_timeout || '10';
            document.getElementById('prusalinkFileDownloadTimeout').value = config.prusalink_file_download_timeout || '60';
            document.getElementById('spoolmanTimeout').value = config.spoolman_timeout || '30';
        })
        .catch(error => {
            console.error('Error loading advanced settings:', error);
        });

    // Load NFC location config
    fetch('/api/nfc/config')
        .then(r => r.json())
        .then(data => {
            var inv = document.getElementById('nfcInventoryLocation');
            var trash = document.getElementById('nfcTrashLocation');
            var syncToggle = document.getElementById('spoolmanLocationSyncEnabled');
            var syncRow = document.getElementById('spoolmanLocationSyncRow');
            if (inv)   inv.value   = data.inventory_location || '';
            if (trash) trash.value = data.trash_location     || '';
            if (syncToggle) {
                syncToggle.checked = !!data.spoolman_location_sync_enabled;
                if (syncRow) syncRow.style.display = syncToggle.checked ? '' : 'none';
            }
            var tapTimeout = document.getElementById('nfcTapTimeout');
            if (tapTimeout) tapTimeout.value = data.tap_timeout_seconds || 15;
        })
        .catch(function() {});
}

function saveNFCConfig() {
    var inv   = (document.getElementById('nfcInventoryLocation')  || {}).value || '';
    var trash = (document.getElementById('nfcTrashLocation')       || {}).value || '';
    var syncEnabled = !!(document.getElementById('spoolmanLocationSyncEnabled') || {}).checked;
    var tapTimeout = parseInt((document.getElementById('nfcTapTimeout') || {value: '15'}).value, 10) || 15;
    fetch('/api/nfc/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ inventory_location: inv, trash_location: trash, spoolman_location_sync_enabled: syncEnabled, tap_timeout_seconds: tapTimeout })
    })
    .then(r => r.json())
    .then(function() { showToast('NFC locations saved.', 'success'); })
    .catch(function(e) { showToast('Failed to save NFC config: ' + e); });
}

function saveAdvancedSettings() {
    const config = {
        prusalink_timeout: document.getElementById('prusalinkTimeout').value,
        prusalink_file_download_timeout: document.getElementById('prusalinkFileDownloadTimeout').value,
        spoolman_timeout: document.getElementById('spoolmanTimeout').value
    };

    // Validate inputs
    if (config.prusalink_timeout < 5 || config.prusalink_timeout > 300) {
        showToast('PrusaLink API timeout must be between 5 and 300 seconds');
        return;
    }
    if (config.prusalink_file_download_timeout < 10 || config.prusalink_file_download_timeout > 600) {
        showToast('File download timeout must be between 10 and 600 seconds');
        return;
    }
    if (config.spoolman_timeout < 5 || config.spoolman_timeout > 300) {
        showToast('Spoolman API timeout must be between 5 and 300 seconds');
        return;
    }

    fetch('/api/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config)
    })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                showToast('Error saving advanced settings: ' + data.error);
            } else {
                showToast('Advanced settings saved successfully! The application will restart to apply changes.', 'success');
                location.reload();
            }
        })
        .catch(error => {
            showToast('Error saving advanced settings: ' + error.message);
        });
}

function resetAdvancedSettings() {
    if (confirm('Reset all timeout settings to their default values?')) {
        document.getElementById('prusalinkTimeout').value = '10';
        document.getElementById('prusalinkFileDownloadTimeout').value = '60';
        document.getElementById('spoolmanTimeout').value = '30';
    }
}

// Auto-Assign Previous Spool Settings Functions

function loadAutoAssignSettings() {
    fetch('/api/config/auto-assign-previous-spool')
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                console.error('Error loading auto-assign settings:', data.error);
                return;
            }
            document.getElementById('autoAssignPreviousSpoolEnabled').checked = data.enabled || false;
        })
        .catch(error => {
            console.error('Error loading auto-assign settings:', error);
        });
}

// Utility Functions
function apiUrl(path) {
    // Ensure path starts with / if not already
    if (!path.startsWith('/')) {
        path = '/' + path;
    }
    return `${window.location.origin}${path}`;
}

// Initialize color swatches based on data-color attributes
function initColorSwatches() {
    document.querySelectorAll('.color-swatch[data-color]').forEach(swatch => {
        const color = swatch.getAttribute('data-color');
        if (color) {
            swatch.style.backgroundColor = '#' + color;
        }
    });
}

// Initialize edit button colors from data attributes
function initEditButtonColors() {
    document.querySelectorAll('.edit-spool-btn[data-color-hex]').forEach(button => {
        const colorHex = button.getAttribute('data-color-hex');
        if (colorHex) {
            button.style.backgroundColor = '#' + colorHex;
            button.style.borderColor = '#' + colorHex;
        }
    });
}

// Convert server timestamps to local time
function convertTimestampsToLocal() {
    const timestampElements = document.querySelectorAll('.error-timestamp');
    timestampElements.forEach(element => {
        const timestampData = element.getAttribute('data-timestamp');
        if (timestampData) {
            const localTime = new Date(timestampData).toLocaleString();
            element.textContent = localTime;
        }
    });
}

// ── Backup & Restore ──────────────────────────────────────────────────────────

let _pendingRestoreFilename = null;

function checkRestorePending() {
    fetch('/api/config')
        .then(r => r.json())
        .then(cfg => {
            const banner = document.getElementById('restorePendingBanner');
            if (banner && cfg.restore_pending === 'true') {
                banner.style.display = 'block';
            }
        })
        .catch(() => {});
}

function loadBackupList() {
    fetch('/api/backup')
        .then(r => r.json())
        .then(entries => {
            const container = document.getElementById('backupListContainer');
            if (!container) return;
            if (!entries || entries.length === 0) {
                container.innerHTML = '<p style="color:var(--text-secondary); font-size:0.9em;">No backups yet.</p>';
                return;
            }
            const rows = entries.map(e => {
                const date = new Date(e.created_at).toLocaleString();
                const size = formatBackupBytes(e.size_bytes);
                return `<tr>
                    <td style="padding:6px 10px;">${e.filename}</td>
                    <td style="padding:6px 10px;">${e.scope}</td>
                    <td style="padding:6px 10px;">${size}</td>
                    <td style="padding:6px 10px;">${date}</td>
                    <td style="padding:6px 10px; white-space:nowrap;">
                        <a class="btn btn-secondary" style="padding:3px 10px; font-size:0.85em; text-decoration:none;"
                           href="/api/backup/${encodeURIComponent(e.filename)}/download" download>⬇ Download</a>
                        <button class="btn" style="padding:3px 10px; font-size:0.85em; background:var(--accent-color,#6c5ce7);"
                           onclick="openRestoreModal('${e.filename}')">↩ Restore</button>
                        <button class="btn btn-danger" style="padding:3px 10px; font-size:0.85em;"
                           onclick="deleteBackup('${e.filename}')">🗑 Delete</button>
                    </td>
                </tr>`;
            }).join('');
            container.innerHTML = `<table style="width:100%; border-collapse:collapse; font-size:0.9em;">
                <thead><tr style="border-bottom:1px solid var(--surface-border);">
                    <th style="text-align:left; padding:6px 10px;">Filename</th>
                    <th style="text-align:left; padding:6px 10px;">Scope</th>
                    <th style="text-align:left; padding:6px 10px;">Size</th>
                    <th style="text-align:left; padding:6px 10px;">Created</th>
                    <th style="text-align:left; padding:6px 10px;">Actions</th>
                </tr></thead>
                <tbody>${rows}</tbody>
            </table>`;
        })
        .catch(() => {
            const container = document.getElementById('backupListContainer');
            if (container) container.innerHTML = '<p style="color:var(--error-color,#e74c3c); font-size:0.9em;">Failed to load backups.</p>';
        });
}

function createBackup() {
    const scope = document.getElementById('backupScope').value;
    const status = document.getElementById('backupCreateStatus');
    const btn = document.getElementById('backupCreateBtn');
    btn.disabled = true;
    status.innerHTML = '<span class="btn-spinner"></span>Creating backup…';
    fetch('/api/backup/create', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({scope})
    })
        .then(r => r.json())
        .then(data => {
            btn.disabled = false;
            if (data.error) { status.textContent = 'Error: ' + data.error; return; }
            status.textContent = 'Created: ' + data.filename;
            loadBackupList();
        })
        .catch(() => { btn.disabled = false; status.textContent = 'Failed to create backup.'; });
}

function uploadBackup() {
    const input = document.getElementById('backupUploadInput');
    const status = document.getElementById('backupUploadStatus');
    const btn = document.getElementById('backupUploadBtn');
    if (!input.files || input.files.length === 0) {
        status.textContent = 'Select a file first.';
        return;
    }
    const formData = new FormData();
    formData.append('file', input.files[0]);
    btn.disabled = true;
    status.innerHTML = '<span class="btn-spinner"></span>Uploading…';
    fetch('/api/backup/upload', {method: 'POST', body: formData})
        .then(r => r.json().then(data => ({status: r.status, data})))
        .then(({status: httpStatus, data}) => {
            btn.disabled = false;
            if (httpStatus === 507) {
                const need = formatBackupBytes(data.estimated_bytes);
                const avail = formatBackupBytes(data.available_bytes);
                status.textContent = `Not enough disk space. Upload needs ~${need} but only ${avail} is available. Free up space and retry.`;
                return;
            }
            if (data.error) { status.textContent = 'Error: ' + data.error; return; }
            status.textContent = 'Uploaded: ' + data.filename;
            input.value = '';
            loadBackupList();
        })
        .catch(() => { btn.disabled = false; status.textContent = 'Upload failed.'; });
}

function loadBackupDiskSpace() {
    const el = document.getElementById('backupDiskSpaceInfo');
    if (!el) return;
    fetch('/api/backup/disk-space')
        .then(r => r.json())
        .then(data => {
            if (data.error) { el.textContent = ''; return; }
            const avail = formatBackupBytes(data.available_bytes);
            const total = data.total_bytes > 0 ? ' of ' + formatBackupBytes(data.total_bytes) : '';
            el.textContent = avail + total + ' available';
        })
        .catch(() => { el.textContent = ''; });
}

function deleteBackup(filename) {
    if (!confirm('Delete backup ' + filename + '?\nThis cannot be undone.')) return;
    fetch('/api/backup/' + encodeURIComponent(filename), {method: 'DELETE'})
        .then(r => r.json())
        .then(data => {
            if (data.error) { showToast('Delete failed: ' + data.error, 'error'); return; }
            showToast('Backup deleted.', 'success');
            loadBackupList();
        })
        .catch(() => showToast('Delete failed.', 'error'));
}

function openRestoreModal(filename) {
    _pendingRestoreFilename = filename;
    const info = document.getElementById('restorePreflightInfo');
    const btn = document.getElementById('restoreConfirmBtn');
    const check = document.getElementById('restoreConfirmCheck');
    const modal = document.getElementById('restoreModal');
    info.textContent = 'Checking…';
    btn.disabled = true;
    check.checked = false;
    modal.style.display = 'flex';

    fetch('/api/backup/' + encodeURIComponent(filename) + '/preflight')
        .then(r => r.json())
        .then(result => {
            if (!result.valid) {
                info.innerHTML = '<span style="color:var(--error-color,#e74c3c);">❌ ' + (result.message || 'Invalid backup') + '</span>';
                return;
            }
            const spaceStatus = result.space_ok
                ? '<span style="color:var(--success);">✓ Sufficient disk space</span>'
                : '<span style="color:var(--error);">✗ ' + result.message + '</span>';
            const replacing = (result.will_replace || []).map(s => '<li style="color:var(--text-primary);">' + s + '</li>').join('');
            info.innerHTML = `
                <span style="color:var(--text-muted); font-size:0.8em; text-transform:uppercase; letter-spacing:0.05em;">Archive</span><br>
                <span style="color:var(--text-primary);">${filename}</span><br><br>
                <span style="color:var(--text-muted); font-size:0.8em; text-transform:uppercase; letter-spacing:0.05em;">Scope</span> &nbsp;
                <span style="color:var(--text-primary);">${result.scope}</span> &nbsp;&nbsp;
                <span style="color:var(--text-muted); font-size:0.8em; text-transform:uppercase; letter-spacing:0.05em;">Size</span> &nbsp;
                <span style="color:var(--text-primary);">${formatBackupBytes(result.uncompressed_bytes)} uncompressed</span><br><br>
                <span style="color:var(--text-muted); font-size:0.8em; text-transform:uppercase; letter-spacing:0.05em;">Disk space</span><br>
                ${spaceStatus}<br><br>
                <span style="color:var(--text-muted); font-size:0.8em; text-transform:uppercase; letter-spacing:0.05em;">Will overwrite (clean replace — no zombie files)</span><ul style="margin:6px 0 0 18px; padding:0;">${replacing}</ul>`;
            if (result.space_ok) {
                check.disabled = false;
                check.onchange = () => { btn.disabled = !check.checked; };
            } else {
                check.disabled = true;
            }
        })
        .catch(() => {
            info.textContent = 'Failed to run preflight check.';
        });
}

function closeRestoreModal() {
    document.getElementById('restoreModal').style.display = 'none';
    _pendingRestoreFilename = null;
}

function confirmRestore() {
    if (!_pendingRestoreFilename) return;
    const btn = document.getElementById('restoreConfirmBtn');
    btn.disabled = true;
    btn.innerHTML = '<span class="btn-spinner"></span>Restoring…';
    fetch('/api/backup/' + encodeURIComponent(_pendingRestoreFilename) + '/restore', {method: 'POST'})
        .then(r => r.json().then(data => ({status: r.status, data})))
        .then(({status: httpStatus, data}) => {
            if (httpStatus === 507) {
                btn.disabled = false;
                btn.textContent = 'Confirm Restore';
                const info = document.getElementById('restorePreflightInfo');
                if (info) {
                    info.innerHTML += '<br><span style="color:var(--error-color,#e74c3c);">✗ Not enough disk space to restore. Free up space and try again.</span>';
                }
                return;
            }
            closeRestoreModal();
            if (data.error) { showToast('Restore failed: ' + data.error, 'error'); return; }
            document.getElementById('restorePendingBanner').style.display = 'block';
            showToast('Restore complete. Restart the service to load the restored data.', 'success');
            loadBackupList();
        })
        .catch(() => {
            closeRestoreModal();
            showToast('Restore request failed.', 'error');
        });
}

function formatBackupBytes(bytes) {
    if (!bytes || bytes < 1024) return (bytes || 0) + ' B';
    const units = ['KB', 'MB', 'GB', 'TB'];
    let v = bytes, i = -1;
    do { v /= 1024; i++; } while (v >= 1024 && i < units.length - 1);
    return v.toFixed(1) + ' ' + units[i];
}

// Initialize everything when page loads
document.addEventListener('DOMContentLoaded', function () {
    convertTimestampsToLocal();
    connectWebSocket();
    initCustomDropdowns();  // needed for server-rendered Spools tab dropdowns
    initColorSwatches();
    initEditButtonColors();

    // Hash-based routing: back/forward and direct URL navigation
    window.addEventListener('hashchange', () => {
        const tab = location.hash.slice(1);
        switchTab(VALID_TABS.includes(tab) ? tab : 'dashboard');
    });

    // Honour hash on initial load; default to dashboard
    const initialTab = location.hash.slice(1);
    switchTab(VALID_TABS.includes(initialTab) ? initialTab : 'dashboard');
});
