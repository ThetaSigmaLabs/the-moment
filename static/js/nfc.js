// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

// FilaBridge Dashboard - NFC Management Functions

// NFC Management Functions
function switchNfcTab(tabName, clickedElement) {
    console.log('Switching to NFC tab:', tabName);
    // Hide all NFC tab contents
    document.querySelectorAll('.nfc-tab-content').forEach(tab => {
        tab.classList.remove('active');
    });

    // Remove active class from all NFC tabs
    document.querySelectorAll('.nfc-tab').forEach(tab => {
        tab.classList.remove('active');
    });

    // Show selected tab content
    document.getElementById(tabName + '-tab').classList.add('active');

    // Add active class to clicked tab
    if (clickedElement) {
        clickedElement.classList.add('active');
    } else {
        // Fallback: find the tab button by onclick attribute
        const tabButtons = document.querySelectorAll('.nfc-tab');
        tabButtons.forEach(btn => {
            if (btn.getAttribute('onclick').includes(tabName)) {
                btn.classList.add('active');
            }
        });
    }

    // Load data for specific tabs
    if (tabName === 'location-tags') {
        console.log('Loading location tags...');
        loadLocationTags();
    }
}

// loadNfcData is kept for compatibility; spool/location tags now load lazily when
// their parent tabs (Spools, Printers) are opened.
async function loadNfcData() {
    // no-op: lazy loading handled by switchTab()
}

// Spool and filament tags are managed in the NFCs tab (nfc_management.js / nfc_tags
// registry). The legacy spool QR viewer was removed in Stage 3, the filament one in Stage 2.

async function loadLocationTags() {
    try {
        console.log('Loading location tags...');
        const response = await fetch('/api/nfc/urls');
        const data = await response.json();
        console.log('NFC URLs data:', data);

        const container = document.getElementById('location-list-container');
        const locationUrls = data.urls.filter(url => url.type === 'location');
        console.log('Location URLs:', locationUrls);

        // Clear container and add informational message
        container.innerHTML = '';

        // Add informational banner about Spoolman locations
        const spoolmanURL = data.spoolman_url || '';
        const messageBanner = document.createElement('div');
        messageBanner.className = 'nfc-info-banner';
        messageBanner.style.cssText = 'background: #fff3cd; border: 1px solid #ffeaa7; color: #856404; padding: 15px; margin-bottom: 15px; border-radius: 8px;';

        let bannerHTML = '<strong>ℹ️ Location Management:</strong><br>';
        bannerHTML += 'Locations represent printer toolheads (e.g. <em>Core One L - T0</em>). ';
        bannerHTML += 'They are written to Spoolman automatically when a spool is assigned to a toolhead. ';
        bannerHTML += 'To add a printer or toolhead, configure it in The Moment settings.';

        messageBanner.innerHTML = bannerHTML;
        container.appendChild(messageBanner);

        if (locationUrls.length === 0) {
            const noLocationsMsg = document.createElement('p');
            noLocationsMsg.textContent = 'No locations available. Create locations in Spoolman to see them here.';
            noLocationsMsg.style.cssText = 'padding: 20px; text-align: center; color: #666;';
            container.appendChild(noLocationsMsg);
            return;
        }

        locationUrls.forEach(url => {
            const item = document.createElement('div');
            item.className = 'nfc-list-item';
            item.dataset.value = url.display_name;
            item.dataset.url = url.url;
            item.dataset.qr = url.qr_code_base64;

            // Determine icon based on location type
            let icon = '📦'; // Storage icon for storage locations
            let iconHtml = icon;
            if (url.location_type === 'printer') {
                iconHtml = '<img src="/static/images/3d-printer-icon.png" alt="3D Printer" style="width: 20px; height: 20px;">';
            }

            item.innerHTML = `
                <div class="location-icon">${iconHtml}</div>
                <div class="item-info">
                    <div class="item-name">${url.display_name}</div>
                </div>
                <div class="location-actions">
                    ${renderLocationActions(url)}
                </div>
            `;

            // Add click handler
            item.addEventListener('click', (e) => {
                // Don't trigger if clicking on action buttons
                if (e.target.closest('.location-actions')) {
                    return;
                }

                // Remove selected class from all items
                container.querySelectorAll('.nfc-list-item').forEach(i => i.classList.remove('selected'));
                // Add selected class to clicked item
                item.classList.add('selected');
                // Show QR code
                displayLocationQR({
                    name: url.display_name,
                    is_printer_location: url.location_type === 'printer',
                    url: url.url,
                    qr_code_base64: url.qr_code_base64,
                    description: url.description || ''
                });
            });

            container.appendChild(item);
        });

        // Initialize search functionality
        initializeLocationSearch(locationUrls);

    } catch (error) {
        console.error('Error loading location tags:', error);
        document.getElementById('location-list-container').innerHTML = '<p>Error loading locations</p>';
    }
}

// Render inline actions for The Moment-managed locations
function renderLocationActions(url) {
    try {
        // Only show actions for non-printer locations (printer locations are virtual)
        if (url.location_type === 'printer') return '';

        const nameAttr = (url.display_name || '').replace(/'/g, "\\'").replace(/"/g, '&quot;');

        // Show rename for all The Moment locations
        let actions = `<a href="javascript:void(0)" onclick="event.preventDefault(); event.stopPropagation(); renameLocation('${nameAttr}');">Rename</a>`;

        // Show delete for local-only locations (not synced to Spoolman)
        if (url.is_local_only) {
            actions += ` • <a href="javascript:void(0)" onclick="event.preventDefault(); event.stopPropagation(); deleteLocation('${nameAttr}');" style="color: #ff6b6b;">Delete</a>`;
        } else {
            actions += ` <span style="color: #666; font-size: 0.9em;">(Synced to Spoolman)</span>`;
        }

        return `<span style="margin-left:8px; font-weight:normal;">${actions}</span>`;
    } catch (error) {
        console.error('Error rendering location actions:', error);
        return '';
    }
}

// Copy URL to clipboard
async function copyUrlToClipboard(urlElementId, buttonElement) {
    try {
        const urlElement = document.getElementById(urlElementId);
        const url = urlElement.textContent;

        if (!url) {
            console.warn('No URL to copy');
            return;
        }

        // Use the Clipboard API
        await navigator.clipboard.writeText(url);

        // Visual feedback - change icon temporarily
        const icon = buttonElement.querySelector('.nfc-copy-icon');
        const originalIcon = icon.textContent;
        icon.textContent = '✓';
        buttonElement.style.background = 'rgba(76, 175, 80, 0.3)';

        // Reset after 2 seconds
        setTimeout(() => {
            icon.textContent = originalIcon;
            buttonElement.style.background = '';
        }, 2000);

    } catch (err) {
        console.error('Failed to copy URL:', err);
        // Fallback for older browsers
        const urlElement = document.getElementById(urlElementId);
        const url = urlElement.textContent;
        const textArea = document.createElement('textarea');
        textArea.value = url;
        textArea.style.position = 'fixed';
        textArea.style.opacity = '0';
        document.body.appendChild(textArea);
        textArea.select();
        try {
            document.execCommand('copy');
            const icon = buttonElement.querySelector('.nfc-copy-icon');
            const originalIcon = icon.textContent;
            icon.textContent = '✓';
            buttonElement.style.background = 'rgba(76, 175, 80, 0.3)';
            setTimeout(() => {
                icon.textContent = originalIcon;
                buttonElement.style.background = '';
            }, 2000);
        } catch (fallbackErr) {
            console.error('Fallback copy failed:', fallbackErr);
            showToast('Failed to copy URL. Please copy manually.', 'error');
        }
        document.body.removeChild(textArea);
    }
}

// Display QR code for selected location
function displayLocationQR(locationData) {
    console.log('Displaying location QR:', locationData);

    // Hide no-selection message
    document.getElementById('location-no-selection').style.display = 'none';

    // Show QR display
    const display = document.getElementById('location-qr-display');
    display.style.display = 'block';

    // Update content
    document.getElementById('location-selected-name').textContent = locationData.name;
    document.getElementById('location-selected-details').innerHTML = `
        <strong>Type:</strong> ${locationData.is_printer_location ? 'Printer Location' : 'Custom Location'}<br>
        ${locationData.description ? `<strong>Description:</strong> ${locationData.description}<br>` : ''}
    `;
    document.getElementById('location-qr-large').src = `data:image/png;base64,${locationData.qr_code_base64}`;
    document.getElementById('location-url-text').textContent = locationData.url;
}

// Initialize search functionality for locations
function initializeLocationSearch(locationUrls) {
    const searchInput = document.getElementById('location-search');
    const container = document.getElementById('location-list-container');

    searchInput.addEventListener('input', (e) => {
        const searchTerm = e.target.value.toLowerCase();
        const items = container.querySelectorAll('.nfc-list-item');

        items.forEach(item => {
            const name = item.querySelector('.item-name').textContent.toLowerCase();
            const details = item.querySelector('.item-details').textContent.toLowerCase();

            if (name.includes(searchTerm) || details.includes(searchTerm)) {
                item.style.display = 'flex';
            } else {
                item.style.display = 'none';
            }
        });
    });
}

// Location Management Functions
async function addLocation() {
    const nameEl = document.getElementById('newLocationName');
    const name = (nameEl.value || '').trim();
    if (!name) { showToast('Please enter a location name'); return; }
    try {
        const url = apiUrl('/api/locations');
        console.log('POST', url, { name });
        const res = await fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
            mode: 'same-origin', credentials: 'same-origin',
            body: JSON.stringify({ name })
        });
        if (!res.ok) throw new Error(await res.text());
        nameEl.value = '';
        await loadLocationTags();
    } catch (e) { console.error(e); showToast(e.message || 'Network error'); }
}

async function renameLocation(currentName) {
    const newName = prompt('Rename location', currentName || '');
    if (!newName || newName.trim() === '' || newName === currentName) return;
    try {
        const url = apiUrl(`/api/locations/${encodeURIComponent(currentName)}`);
        console.log('PUT', url, { name: newName.trim() });
        const res = await fetch(url, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
            mode: 'same-origin', credentials: 'same-origin',
            body: JSON.stringify({ name: newName.trim() })
        });
        if (!res.ok) {
            const errorText = await res.text();
            throw new Error(errorText);
        }
        const result = await res.json();
        console.log('Rename result:', result);
        await loadLocationTags();
        if (result.message) {
            showToast(result.message);
        }
    } catch (e) {
        console.error('Rename error:', e);
        showToast(e.message || 'Network error');
    }
}

async function deleteLocation(name) {
    try {
        console.log('deleteLocation called with name:', name);
        const url = apiUrl(`/api/locations/${encodeURIComponent(name)}`);
        console.log('DELETE', url);
        const res = await fetch(url, {
            method: 'DELETE',
            headers: { 'Accept': 'application/json' },
            mode: 'same-origin', credentials: 'same-origin'
        });
        if (!res.ok) {
            const errorText = await res.text();
            throw new Error(errorText);
        }
        const result = await res.json();
        console.log('Delete result:', result);
        await loadLocationTags();
    } catch (e) {
        console.error('Delete error:', e);
        showToast(e.message || 'Network error');
    }
}


// ─── OpenPrintTag Field Editor ────────────────────────────────────────────────

let _optSpoolID = null;

function openSpoolTagEditor() {
    const selectedItem = document.querySelector('#spool-list-container .nfc-list-item.selected');
    if (!selectedItem) { showToast('Select a spool first.'); return; }
    _optSpoolID = selectedItem.dataset.value;
    if (!_optSpoolID) { showToast('No spool ID found.'); return; }

    _ensureOptModal();
    document.getElementById('opt-modal').style.display = 'flex';
    document.getElementById('opt-status').textContent = 'Loading fields…';
    document.getElementById('opt-form').style.display = 'none';

    fetch(`/api/nfc/spools/${_optSpoolID}/fields`)
        .then(r => r.json())
        .then(data => {
            document.getElementById('opt-actual-weight').value = data.nfc_actual_weight || '';
            document.getElementById('opt-manufacturing-date').value = data.nfc_manufacturing_date || '';
            document.getElementById('opt-expiration-date').value = data.nfc_expiration_date || '';
            document.getElementById('opt-material-class').value = data.nfc_material_class || 'FFF';
            document.getElementById('opt-min-print-temp').value = data.nfc_min_print_temp || '';
            document.getElementById('opt-max-print-temp').value = data.nfc_max_print_temp || '';
            document.getElementById('opt-min-bed-temp').value = data.nfc_min_bed_temp || '';
            document.getElementById('opt-max-bed-temp').value = data.nfc_max_bed_temp || '';
            document.getElementById('opt-country').value = data.nfc_country_of_origin || '';
            document.getElementById('opt-mat-props').value = data.nfc_material_properties || '';
            document.getElementById('opt-transmission').value = data.nfc_transmission_distance || '';
            document.getElementById('opt-nominal-length').value = data.nfc_nominal_length || '';
            document.getElementById('opt-status').textContent = '';
            document.getElementById('opt-form').style.display = 'block';
        })
        .catch(e => {
            document.getElementById('opt-status').textContent = 'Error loading fields: ' + e;
        });
}

async function saveAndDownloadSpoolTag() {
    if (!_optSpoolID) return;
    const btn = document.getElementById('opt-save-btn');
    btn.disabled = true;
    btn.textContent = 'Saving…';

    const body = {
        nfc_actual_weight:        parseFloat(document.getElementById('opt-actual-weight').value) || 0,
        nfc_manufacturing_date:   document.getElementById('opt-manufacturing-date').value,
        nfc_expiration_date:      document.getElementById('opt-expiration-date').value,
        nfc_material_class:       document.getElementById('opt-material-class').value,
        nfc_min_print_temp:       parseInt(document.getElementById('opt-min-print-temp').value) || 0,
        nfc_max_print_temp:       parseInt(document.getElementById('opt-max-print-temp').value) || 0,
        nfc_min_bed_temp:         parseInt(document.getElementById('opt-min-bed-temp').value) || 0,
        nfc_max_bed_temp:         parseInt(document.getElementById('opt-max-bed-temp').value) || 0,
        nfc_country_of_origin:    document.getElementById('opt-country').value,
        nfc_material_properties:  document.getElementById('opt-mat-props').value,
        nfc_transmission_distance: parseFloat(document.getElementById('opt-transmission').value) || 0,
        nfc_nominal_length:       parseInt(document.getElementById('opt-nominal-length').value) || 0,
    };

    try {
        const resp = await fetch(`/api/nfc/spools/${_optSpoolID}/fields`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        if (!resp.ok) throw new Error(await resp.text());

        // Trigger .bin download
        window.location.href = `/api/nfc/spool-tag/${_optSpoolID}`;
        closeOptModal();
    } catch (e) {
        showToast('Error: ' + e);
    } finally {
        btn.disabled = false;
        btn.textContent = 'Save to Spoolman & Download .bin';
    }
}

function closeOptModal() {
    const m = document.getElementById('opt-modal');
    if (m) m.style.display = 'none';
}

function _ensureOptModal() {
    if (document.getElementById('opt-modal')) return;
    const modal = document.createElement('div');
    modal.id = 'opt-modal';
    modal.style.cssText = 'display:none; position:fixed; inset:0; background:rgba(0,0,0,0.6); z-index:1000; align-items:center; justify-content:center;';
    modal.addEventListener('click', e => { if (e.target === modal) closeOptModal(); });
    modal.innerHTML = `
      <div style="background:#1e1e2e; border-radius:10px; padding:24px; width:min(540px,95vw); max-height:90vh; overflow-y:auto; color:#cdd6f4;">
        <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:16px;">
          <h3 style="margin:0;">📲 OpenPrintTag Fields</h3>
          <button onclick="closeOptModal()" style="background:none;border:none;color:#cdd6f4;font-size:1.4em;cursor:pointer;">✕</button>
        </div>
        <p style="font-size:0.85em;color:#888;margin:0 0 16px;">Fields are saved to Spoolman, then the CBOR+URL .bin is downloaded. Write it to your ICODE SLIX2 tag via NFC Tools "Write Dump".</p>
        <div id="opt-status" style="color:#f38ba8; margin-bottom:12px;"></div>
        <div id="opt-form" style="display:none;">
          <p style="font-size:0.8em; font-weight:bold; color:#89b4fa; margin:0 0 8px; text-transform:uppercase; letter-spacing:0.05em;">Spool details</p>
          <div style="display:grid; grid-template-columns:1fr 1fr; gap:10px; margin-bottom:16px;">
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Actual Weight (g)
              <input id="opt-actual-weight" type="number" step="0.1" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <div></div>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Manufacturing Date
              <input id="opt-manufacturing-date" type="date" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Expiration Date
              <input id="opt-expiration-date" type="date" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
          </div>
          <p style="font-size:0.8em; font-weight:bold; color:#89b4fa; margin:0 0 8px; text-transform:uppercase; letter-spacing:0.05em;">Print settings (filament)</p>
          <div style="display:grid; grid-template-columns:1fr 1fr; gap:10px;">
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Material Class
              <input id="opt-material-class" type="text" placeholder="FFF" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Country of Origin
              <input id="opt-country" type="text" placeholder="CZ" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Min Print Temp (°C)
              <input id="opt-min-print-temp" type="number" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Max Print Temp (°C)
              <input id="opt-max-print-temp" type="number" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Min Bed Temp (°C)
              <input id="opt-min-bed-temp" type="number" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Max Bed Temp (°C)
              <input id="opt-max-bed-temp" type="number" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Nominal Length (mm)
              <input id="opt-nominal-length" type="number" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em;">
              Transmission Distance
              <input id="opt-transmission" type="number" step="0.001" style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
            <label style="display:flex;flex-direction:column;gap:4px;font-size:0.85em; grid-column:1/-1;">
              Material Properties (JSON array, e.g. ["abrasive"])
              <input id="opt-mat-props" type="text" placeholder='["matte"]' style="padding:6px 8px;background:#313244;border:1px solid #45475a;border-radius:6px;color:#cdd6f4;">
            </label>
          </div>
          <div style="display:flex; gap:10px; margin-top:20px;">
            <button id="opt-save-btn" onclick="saveAndDownloadSpoolTag()" style="flex:1; padding:10px; background:#7c3aed; color:#fff; border:none; border-radius:6px; cursor:pointer; font-size:0.95em;">
              Save to Spoolman &amp; Download .bin
            </button>
            <button onclick="closeOptModal()" style="padding:10px 16px; background:#313244; color:#cdd6f4; border:none; border-radius:6px; cursor:pointer;">
              Cancel
            </button>
          </div>
        </div>
      </div>
    `;
    document.body.appendChild(modal);
}

// QR Code Modal Functions
function showQrCode(url, title, qrCodeBase64) {
    // Create modal if it doesn't exist
    let modal = document.getElementById('nfc-qr-modal');
    if (!modal) {
        modal = document.createElement('div');
        modal.id = 'nfc-qr-modal';
        modal.className = 'nfc-qr-modal';
        modal.innerHTML = `
            <div class="nfc-qr-content">
                <h3 id="qr-title"></h3>
                <div class="nfc-qr-modal-code" id="qr-code"></div>
                <div class="nfc-url" id="qr-url"></div>
                <div class="nfc-instructions">
                    <h4>Instructions:</h4>
                    <ol>
                        <li>Open NFC Tools Pro on your phone</li>
                        <li>Scan this QR code to copy the URL</li>
                        <li>Write the URL to your NFC tag</li>
                    </ol>
                </div>
                <button class="btn" onclick="closeQrModal()">Close</button>
            </div>
        `;
        document.body.appendChild(modal);
    }

    // Update modal content
    document.getElementById('qr-title').textContent = title;
    document.getElementById('qr-url').textContent = url;

    // Display real QR code or placeholder
    const qrCodeDiv = document.getElementById('qr-code');
    if (qrCodeBase64 && qrCodeBase64 !== '') {
        qrCodeDiv.innerHTML = `<img src="data:image/png;base64,${qrCodeBase64}" alt="QR Code" style="width: 256px; height: 256px; border-radius: 8px; box-shadow: 0 4px 12px rgba(0,0,0,0.15);">`;
    } else {
        // Fallback placeholder if QR code generation failed
        qrCodeDiv.innerHTML = `<div style="width: 256px; height: 256px; background: #f0f0f0; display: flex; align-items: center; justify-content: center; border: 2px dashed #ccc; border-radius: 8px;">
            <div style="text-align: center;">
                <div style="font-size: 48px; margin-bottom: 10px;">⚠️</div>
                <div style="font-size: 12px; color: #666;">QR Code Error</div>
                <div style="font-size: 10px; color: #999;">Copy URL manually</div>
            </div>
        </div>`;
    }

    // Show modal
    modal.style.display = 'block';
}

function closeQrModal() {
    const modal = document.getElementById('nfc-qr-modal');
    if (modal) {
        modal.style.display = 'none';
    }
}

// Close modal when clicking outside
window.onclick = function (event) {
    const modal = document.getElementById('nfc-qr-modal');
    if (event.target === modal) {
        closeQrModal();
    }
}

