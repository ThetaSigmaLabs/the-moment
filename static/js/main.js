// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

// The Moment Dashboard - Main JavaScript Functions

// Tab switching functionality
function switchTab(tabName) {
    // Hide all tab contents
    document.querySelectorAll('.tab-content').forEach(content => {
        content.classList.remove('active');
    });

    // Remove active class from all tabs
    document.querySelectorAll('.tab').forEach(tab => {
        tab.classList.remove('active');
    });

    // Show selected tab content
    document.getElementById(tabName + '-tab').classList.add('active');

    // Add active class to clicked tab
    event.target.classList.add('active');

    if (tabName === 'dashboard') {
        loadDashboardStats();
    }

    // Sync Spoolman locations when Spools tab is opened so changes made in
    // Spoolman are reflected immediately rather than waiting for the 5-min poll.
    if (tabName === 'spools') {
        fetch('/api/nfc/sync-locations-now', { method: 'POST' }).catch(() => {});
        loadSpoolTags();
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
}

function toggleConfig() {
    // Switch to the settings tab
    switchTab('settings');
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
    } else if (tabName === 'advanced') {
        loadAdvancedSettings();
        loadAutoAssignSettings();
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
                        <label><strong>Spoolman URL:</strong></label>
                        <input type="text" id="spoolman_url" value="${config.spoolman_url || ''}" placeholder="http://localhost:8000">
                        <small>URL where Spoolman is running</small>
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
    const url = document.getElementById('spoolman_url').value.trim();
    const resultEl = document.getElementById('spoolman-test-result');

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
        })
        .catch(function() {});
}

function saveNFCConfig() {
    var inv   = (document.getElementById('nfcInventoryLocation')  || {}).value || '';
    var trash = (document.getElementById('nfcTrashLocation')       || {}).value || '';
    var syncEnabled = !!(document.getElementById('spoolmanLocationSyncEnabled') || {}).checked;
    fetch('/api/nfc/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ inventory_location: inv, trash_location: trash, spoolman_location_sync_enabled: syncEnabled })
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

// Initialize everything when page loads
document.addEventListener('DOMContentLoaded', function () {
    convertTimestampsToLocal();
    connectWebSocket();
    initCustomDropdowns();  // needed for server-rendered Spools tab dropdowns
    initColorSwatches();
    initEditButtonColors();
    loadDashboardStats();   // populate Dashboard tab on first load
});
