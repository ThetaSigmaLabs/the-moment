// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

// The Moment Dashboard - Main JavaScript Functions

// Tab switching functionality
function switchTab(tabName) {
    // Hide all tab contents
    const tabContents = document.querySelectorAll('.tab-content');
    tabContents.forEach(content => {
        content.classList.remove('active');
    });

    // Remove active class from all tabs
    const tabs = document.querySelectorAll('.tab');
    tabs.forEach(tab => {
        tab.classList.remove('active');
    });

    // Show selected tab content
    document.getElementById(tabName + '-tab').classList.add('active');

    // Add active class to clicked tab
    event.target.classList.add('active');

    // Load configuration when settings tab is opened
    if (tabName === 'settings') {
        // Load data for the currently active settings sub-tab
        const activeSettingsTab = document.querySelector('.settings-tab.active');
        if (activeSettingsTab) {
            // Determine which tab is active and load its data
            const activeTabContent = document.querySelector('.settings-tab-content.active');
            if (activeTabContent) {
                const tabId = activeTabContent.id.replace('-tab', '');
                if (tabId === 'getting-started') {
                    // Getting Started tab doesn't need data loading
                } else if (tabId === 'basic-config') {
                    loadConfiguration();
                } else if (tabId === 'printers') {
                    loadPrinters();
                } else if (tabId === 'advanced') {
                    loadAdvancedSettings();
                    loadAutoAssignSettings();
                }
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
    if (tabName === 'getting-started') {
        // Getting Started tab doesn't need data loading
    } else if (tabName === 'basic-config') {
        loadConfiguration();
    } else if (tabName === 'printers') {
        loadPrinters();
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
                alert('Error saving configuration: ' + data.error);
            } else {
                alert('Configuration saved successfully! The Moment will restart.');
                location.reload();
            }
        })
        .catch(error => {
            alert('Error saving configuration: ' + error.message);
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
    const locationSelect = document.getElementById('autoAssignPreviousSpoolLocation');
    const location = locationSelect ? locationSelect.value.trim() : '';

    const settings = {
        enabled: enabled,
        location: location
    };

    fetch('/api/config/auto-assign-previous-spool', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(settings)
    })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                alert('Error saving auto-assign settings: ' + data.error);
            } else {
                alert('Auto-assign settings saved successfully!');
            }
        })
        .catch(error => {
            alert('Error saving auto-assign settings: ' + error.message);
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
                alert('Error saving cost settings: ' + data.error);
            } else {
                alert('Cost settings saved successfully!');
                if (window.costCalculator) {
                    window.costCalculator.loadSettings();
                }
            }
        })
        .catch(error => {
            alert('Error saving cost settings: ' + error.message);
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
            if (inv)   inv.value   = data.inventory_location || '';
            if (trash) trash.value = data.trash_location     || '';
        })
        .catch(function() {});
}

function saveNFCConfig() {
    var inv   = (document.getElementById('nfcInventoryLocation')  || {}).value || '';
    var trash = (document.getElementById('nfcTrashLocation')       || {}).value || '';
    fetch('/api/nfc/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ inventory_location: inv, trash_location: trash })
    })
    .then(r => r.json())
    .then(function() { alert('NFC locations saved.'); })
    .catch(function(e) { alert('Failed to save NFC config: ' + e); });
}

function saveAdvancedSettings() {
    const config = {
        prusalink_timeout: document.getElementById('prusalinkTimeout').value,
        prusalink_file_download_timeout: document.getElementById('prusalinkFileDownloadTimeout').value,
        spoolman_timeout: document.getElementById('spoolmanTimeout').value
    };

    // Validate inputs
    if (config.prusalink_timeout < 5 || config.prusalink_timeout > 300) {
        alert('PrusaLink API timeout must be between 5 and 300 seconds');
        return;
    }
    if (config.prusalink_file_download_timeout < 10 || config.prusalink_file_download_timeout > 600) {
        alert('File download timeout must be between 10 and 600 seconds');
        return;
    }
    if (config.spoolman_timeout < 5 || config.spoolman_timeout > 300) {
        alert('Spoolman API timeout must be between 5 and 300 seconds');
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
                alert('Error saving advanced settings: ' + data.error);
            } else {
                alert('Advanced settings saved successfully! The application will restart to apply changes.');
                location.reload();
            }
        })
        .catch(error => {
            alert('Error saving advanced settings: ' + error.message);
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
// Store the checkbox change handler so we can remove it before adding a new one
let autoAssignCheckboxHandler = null;

function loadAutoAssignSettings() {
    // First, load the settings
    fetch('/api/config/auto-assign-previous-spool')
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                console.error('Error loading auto-assign settings:', data.error);
                return;
            }

            const enabled = data.enabled || false;
            const location = data.location || '';

            document.getElementById('autoAssignPreviousSpoolEnabled').checked = enabled;

            // Show/hide location dropdown based on checkbox
            const locationGroup = document.getElementById('autoAssignLocationGroup');
            if (locationGroup) {
                locationGroup.style.display = enabled ? 'block' : 'none';
            }

            // Load locations and populate dropdown
            return fetch('/api/locations')
                .then(response => response.json())
                .then(locationsData => {
                    if (locationsData.error) {
                        console.error('Error loading locations:', locationsData.error);
                        return;
                    }

                    const locationSelect = document.getElementById('autoAssignPreviousSpoolLocation');
                    if (!locationSelect) return;

                    // Clear existing options except the first one
                    locationSelect.innerHTML = '<option value="">Select a location...</option>';

                    // Filter out printer toolhead locations (we only want storage locations)
                    const storageLocations = locationsData.locations.filter(loc => {
                        return !loc.is_virtual && loc.type !== 'printer';
                    });

                    // Sort locations alphabetically by name
                    storageLocations.sort((a, b) => {
                        const nameA = (a.name || '').toLowerCase();
                        const nameB = (b.name || '').toLowerCase();
                        return nameA.localeCompare(nameB);
                    });

                    // Add locations to dropdown
                    storageLocations.forEach(loc => {
                        const option = document.createElement('option');
                        option.value = loc.name;
                        option.textContent = loc.name;
                        if (loc.name === location) {
                            option.selected = true;
                        }
                        locationSelect.appendChild(option);
                    });

                    // If the saved location is not in the list (e.g., it was deleted), add it as selected
                    if (location && !storageLocations.find(loc => loc.name === location)) {
                        const option = document.createElement('option');
                        option.value = location;
                        option.textContent = location + ' (not found)';
                        option.selected = true;
                        locationSelect.appendChild(option);
                    }
                })
                .catch(error => {
                    console.error('Error loading locations:', error);
                });
        })
        .then(() => {
            // Set up checkbox change handler
            const checkbox = document.getElementById('autoAssignPreviousSpoolEnabled');
            const locationGroup = document.getElementById('autoAssignLocationGroup');

            if (checkbox && locationGroup) {
                // Remove existing event listener if it exists
                if (autoAssignCheckboxHandler) {
                    checkbox.removeEventListener('change', autoAssignCheckboxHandler);
                }

                // Create and store the new handler function
                autoAssignCheckboxHandler = function () {
                    locationGroup.style.display = this.checked ? 'block' : 'none';
                };

                // Add the event listener
                checkbox.addEventListener('change', autoAssignCheckboxHandler);
            }
        })
        .catch(error => {
            console.error('Error loading auto-assign settings:', error);
        });
}

function saveAutoAssignSettings() {
    const enabled = document.getElementById('autoAssignPreviousSpoolEnabled').checked;
    const locationSelect = document.getElementById('autoAssignPreviousSpoolLocation');
    const location = locationSelect ? locationSelect.value.trim() : '';

    const settings = {
        enabled: enabled,
        location: location
    };

    fetch('/api/config/auto-assign-previous-spool', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(settings)
    })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                alert('Error saving auto-assign settings: ' + data.error);
            } else {
                alert('Auto-assign settings saved successfully!');
            }
        })
        .catch(error => {
            alert('Error saving auto-assign settings: ' + error.message);
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
    loadNfcData();
    loadPrinters();
    initCustomDropdowns();
    initColorSwatches();
    initEditButtonColors();
});
