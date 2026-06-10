// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — Cost Settings & Calculation UI

// ─── State ────────────────────────────────────────────────────────────────────

// Stored between processFile() call and calculateProcessCost() call
var _lastProcessResult = null; // { filamentGrams, spoolId, usage }

// ─── Settings Tab ─────────────────────────────────────────────────────────────

function loadCostSettings() {
    fetch('/api/cost-settings')
        .then(function(r) { return r.json(); })
        .then(function(s) {
            _setVal('cost-currency',         s.currency         || 'USD');
            _setVal('cost-electricity-rate', s.electricity_rate || 0.12);
            _setVal('cost-wattage',          s.printer_wattage  || 150);
            _setVal('cost-maintenance',      s.maintenance_rate || 0.10);
            _setVal('cost-depreciation',     s.depreciation_rate|| 0.05);
            _setVal('cost-margin',           s.margin_percent   || 0);
        })
        .catch(function(err) {
            console.error('Failed to load cost settings:', err);
        });
}

function saveCostSettings() {
    var settings = {
        currency:          (_getVal('cost-currency') || 'USD').toUpperCase().trim(),
        electricity_rate:  parseFloat(_getVal('cost-electricity-rate')) || 0,
        printer_wattage:   parseFloat(_getVal('cost-wattage'))          || 0,
        maintenance_rate:  parseFloat(_getVal('cost-maintenance'))      || 0,
        depreciation_rate: parseFloat(_getVal('cost-depreciation'))     || 0,
        margin_percent:    parseFloat(_getVal('cost-margin'))           || 0
    };

    fetch('/api/cost-settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(settings)
    })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { showToast('Error: ' + data.error); return; }
            var btn = document.querySelector('button[onclick="saveCostSettings()"]');
            if (btn) {
                var orig = btn.textContent;
                btn.textContent = '✅ Saved!';
                setTimeout(function() { btn.textContent = orig; }, 1800);
            }
        })
        .catch(function(err) { showToast('Error saving: ' + err.message); });
}

// Quick calculator on the settings page
function runQuickCalc() {
    var grams   = parseFloat(document.getElementById('calc-grams').value)   || 0;
    var minutes = parseFloat(document.getElementById('calc-minutes').value)  || 0;
    var priceKg = parseFloat(document.getElementById('calc-price-kg').value) || 0;

    // Build a synthetic request — spoolID 0, override price via a temporary
    // mechanism. We calculate client-side using current field values.
    var elecRate  = parseFloat(_getVal('cost-electricity-rate')) || 0;
    var wattage   = parseFloat(_getVal('cost-wattage'))          || 0;
    var maint     = parseFloat(_getVal('cost-maintenance'))      || 0;
    var deprec    = parseFloat(_getVal('cost-depreciation'))     || 0;
    var margin    = parseFloat(_getVal('cost-margin'))           || 0;
    var currency  = (_getVal('cost-currency') || 'USD').toUpperCase();

    var hours = minutes / 60;
    var filCost   = (grams / 1000) * priceKg;
    var elecCost  = (wattage / 1000) * hours * elecRate;
    var maintCost = hours * maint;
    var deprecCost= hours * deprec;
    var sub       = filCost + elecCost + maintCost + deprecCost;
    var mrgAmt    = sub * (margin / 100);
    var total     = sub + mrgAmt;

    var el = document.getElementById('quick-calc-result');
    if (el) {
        el.style.display = 'block';
        el.innerHTML = _renderCostRows({
            filament_cost:     filCost,
            electricity_cost:  elecCost,
            maintenance_cost:  maintCost,
            depreciation_cost: deprecCost,
            sub_total:         sub,
            margin_amount:     mrgAmt,
            total_cost:        total,
            filament_price_per_kg: priceKg,
            filament_grams:    grams,
            print_time_min:    minutes,
            currency:          currency
        }, currency);
    }
}

// ─── Process Result Modal cost section ────────────────────────────────────────

// Called by printers.js after a successful process — stores state and reveals button
function afterProcessSuccess(filamentGrams, spoolId) {
    _lastProcessResult = { filamentGrams: filamentGrams, spoolId: spoolId || 0 };

    var btn = document.getElementById('costToggleBtn');
    if (btn) btn.style.display = '';

    // Auto-show if grams > 0
    if (filamentGrams > 0) {
        showCostSection();
    }
}

function toggleCostSection() {
    var sec = document.getElementById('processCostSection');
    if (!sec) return;
    if (sec.style.display === 'none') {
        showCostSection();
    } else {
        sec.style.display = 'none';
    }
}

function showCostSection() {
    var sec = document.getElementById('processCostSection');
    if (sec) sec.style.display = 'block';

    // Pre-fill print time from gcode metadata if available (set by printers.js)
    var ptEl = document.getElementById('costPrintTime');
    if (ptEl && window._lastGcodePrintTimeMin) {
        ptEl.value = Math.round(window._lastGcodePrintTimeMin);
    }
}

function calculateProcessCost() {
    if (!_lastProcessResult) return;

    var printTimeMin = parseFloat(document.getElementById('costPrintTime').value) || 0;
    var priceKgOverride = parseFloat(document.getElementById('costPriceKg').value) || -1;

    // Always fetch from API so server-side settings are used
    fetch('/api/cost/calculate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            filament_grams: _lastProcessResult.filamentGrams,
            print_time_min: printTimeMin,
            spool_id:       _lastProcessResult.spoolId
        })
    })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) {
                document.getElementById('costBreakdownRows').innerHTML =
                    '<p style="color:#ef9a9a;">Error: ' + data.error + '</p>';
                return;
            }
            // Override filament price if user typed one
            if (priceKgOverride >= 0) {
                var g = _lastProcessResult.filamentGrams;
                data.filament_cost = Math.round((g / 1000) * priceKgOverride * 10000) / 10000;
                data.filament_price_per_kg = priceKgOverride;
                // Recalculate totals
                data.sub_total = data.filament_cost + data.electricity_cost +
                                 data.maintenance_cost + data.depreciation_cost;
                data.margin_amount = data.sub_total * (data.settings.margin_percent / 100);
                data.total_cost = data.sub_total + data.margin_amount;
            }
            document.getElementById('costBreakdownRows').innerHTML =
                _renderCostRows(data, data.currency);
        })
        .catch(function(err) {
            document.getElementById('costBreakdownRows').innerHTML =
                '<p style="color:#ef9a9a;">Request failed: ' + err.message + '</p>';
        });
}

// ─── Rendering ────────────────────────────────────────────────────────────────

function _renderCostRows(d, currency) {
    var fmt = function(n) {
        return new Intl.NumberFormat('en-US', {
            style: 'currency', currency: currency || 'USD', minimumFractionDigits: 2
        }).format(n || 0);
    };
    var row = function(label, val, dim) {
        return '<div style="display:flex;justify-content:space-between;padding:5px 0;' +
               'border-bottom:1px solid #2a2a2a;">' +
               '<span style="color:#bbb;">' + label + '</span>' +
               '<span style="color:#d0d0d0;">' + val + (dim ? ' <span style="color:#777;font-size:0.8em;">' + dim + '</span>' : '') + '</span>' +
               '</div>';
    };

    var html = '';

    // ── Filament section: per-spool order-sheet rows (if available) ──────────────
    if (d.filament_lines && d.filament_lines.length > 0) {
        var hasSwaps = d.filament_lines.some(function(l) { return l.change_number > 0; });
        var noPriceAny = false;
        html += '<div style="margin-bottom:10px;">' +
            '<div style="font-size:0.75em;color:#777;text-transform:uppercase;letter-spacing:0.06em;margin-bottom:6px;">Filament</div>' +
            '<table style="width:100%;font-size:0.875em;border-collapse:collapse;">' +
            '<tr style="color:#888;font-size:0.78em;border-bottom:1px solid #333;">' +
            '<th style="text-align:left;padding:4px 8px 4px 0;font-weight:500;">Tool</th>' +
            '<th style="text-align:right;padding:4px 8px;font-weight:500;">Grams</th>' +
            '<th style="text-align:right;padding:4px 0 4px 8px;font-weight:500;">$/kg</th>' +
            '<th style="text-align:right;padding:4px 0 4px 8px;font-weight:500;">Cost</th>' +
            '</tr>';
        d.filament_lines.forEach(function(l) {
            var toolLabel = 'T' + l.tool_index + (hasSwaps && l.change_number > 0 ? ' · swap #' + l.change_number : '');
            var noPrice = !l.price_per_kg || l.price_per_kg === 0;
            if (noPrice) noPriceAny = true;
            html += '<tr style="border-top:1px solid #2a2a2a;">' +
                '<td style="padding:5px 8px 5px 0;color:#ccc;white-space:nowrap;">' + toolLabel + '</td>' +
                '<td style="padding:5px 8px;text-align:right;color:#c8b8ff;">' + (l.grams || 0).toFixed(2) + ' g</td>' +
                '<td style="padding:5px 0 5px 8px;text-align:right;color:#aaa;">' +
                    (noPrice ? '<span style="color:#555;">—</span>' : fmt(l.price_per_kg) + '/kg') + '</td>' +
                '<td style="padding:5px 0 5px 8px;text-align:right;color:#c8b8ff;">' +
                    (noPrice ? '<span style="color:#555;">—</span>' : fmt(l.cost)) + '</td>' +
                '</tr>';
        });
        if (d.filament_lines.length > 1) {
            html += '<tr style="border-top:1px solid #444;">' +
                '<td colspan="3" style="padding:5px 8px 5px 0;text-align:right;color:#888;font-size:0.9em;">Filament total</td>' +
                '<td style="padding:5px 0 5px 8px;text-align:right;font-weight:600;color:#c8b8ff;">' + fmt(d.filament_cost) + '</td>' +
                '</tr>';
        }
        html += '</table></div>';
        if (noPriceAny) {
            html += '<p style="color:#ffb74d;font-size:0.8em;margin:0 0 8px;">' +
                    '⚠️ One or more spools have no price set in Spoolman.</p>';
        }
    } else {
        // Fallback: single aggregate filament row (legacy / no per-spool data)
        if (d.filament_grams !== undefined) {
            html += row('Filament used', (d.filament_grams || 0).toFixed(2) + ' g');
        }
        if (d.filament_price_per_kg !== undefined && d.filament_price_per_kg > 0) {
            html += row('Filament cost', fmt(d.filament_cost),
                        '(' + fmt(d.filament_price_per_kg) + '/kg)');
        } else {
            html += row('Filament cost', fmt(d.filament_cost), '(no price in Spoolman)');
        }
    }

    // ── Print costs ──────────────────────────────────────────────────────────────
    if (d.print_time_min !== undefined) {
        html += row('Print time', _fmtMin(d.print_time_min));
    }
    if (d.preheat_cost !== undefined && d.preheat_cost > 0) {
        html += row('Preheat', fmt(d.preheat_cost));
    }
    var elecLabel = 'Electricity';
    if (d.high_temp_applied) elecLabel += ' ⚡ high-temp';
    html += row(elecLabel, fmt(d.electricity_cost));
    html += row('Maintenance', fmt(d.maintenance_cost));
    if (d.depreciation_cost !== undefined) {
        html += row('Depreciation', fmt(d.depreciation_cost));
    }
    html += row('Subtotal', fmt(d.sub_total));
    if (d.margin_amount > 0) {
        var pct = d.settings ? d.settings.margin_percent : 0;
        html += row('Margin', fmt(d.margin_amount), '(' + pct + '%)');
    }

    html += '<div style="display:flex;justify-content:space-between;padding:8px 0;' +
            'border-top:2px solid #444;margin-top:4px;font-weight:700;font-size:1.05em;">' +
            '<span style="color:#d0d0d0;">Total</span><span style="color:#c8b8ff;">' + fmt(d.total_cost) + '</span></div>';

    return html;
}

function _fmtMin(min) {
    if (!min) return '0 min';
    var h = Math.floor(min / 60);
    var m = Math.round(min % 60);
    return h > 0 ? h + 'h ' + m + 'm' : m + ' min';
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function _setVal(id, val) {
    var el = document.getElementById(id);
    if (el) el.value = val;
}

function _getVal(id) {
    var el = document.getElementById(id);
    return el ? el.value : '';
}

// ─── Per-printer cost settings ────────────────────────────────────────────────

function loadPrinterCostSettings() {
    fetch('/api/cost-settings/printers')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var printers = data.printers || [];
            var container = document.getElementById('printerCostCards');
            if (!container) return;
            if (printers.length === 0) {
                container.innerHTML = '<p style="color:#666;font-size:0.9em;">No printers configured yet.</p>';
                return;
            }
            container.innerHTML = printers.map(function(p) {
                return buildPrinterCostCard(p);
            }).join('');
        })
        .catch(function(err) {
            var c = document.getElementById('printerCostCards');
            if (c) c.innerHTML = '<p style="color:#ef9a9a;">Failed to load: ' + err.message + '</p>';
        });
}

function buildPrinterCostCard(p) {
    var name = p.printer_name || '';
    var safe = name.replace(/[^a-zA-Z0-9_-]/g, '_');
    // Derive display depreciation
    var derivedDepreciation = '';
    if (p.printer_purchase_cost > 0 && p.estimated_life_hrs > 0) {
        derivedDepreciation = ' (≈ ' + _fmtCurrencySimple(p.printer_purchase_cost / p.estimated_life_hrs) + '/hr derived)';
    }

    return '<div style="border:1px solid #333;border-radius:8px;padding:16px;background:rgba(255,255,255,0.03);">' +
        '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px;">' +
        '<strong style="font-size:0.95em;">' + _escHtml(name) + '</strong>' +
        '<button class="btn btn-small" onclick="savePrinterCostSettings(' + JSON.stringify(name) + ')">💾 Save</button>' +
        '</div>' +
        '<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:10px;">' +

        _pcField(safe, 'print_wattage_w',       'Print wattage (W)',           p.print_wattage_w,       1,   'Steady-state draw during printing. 0 = use global.') +
        _pcField(safe, 'preheat_wattage_w',     'Preheat wattage (W)',         p.preheat_wattage_w,     1,   'Watts during warmup — usually 2–3× print wattage.') +
        _pcField(safe, 'preheat_time_min',       'Preheat time (min)',           p.preheat_time_min,      0.5, 'Minutes to reach print temperature. One-time cost per print.') +
        _pcField(safe, 'high_temp_extra_w',      'High-temp extra (W)',         p.high_temp_extra_w,     1,   'Added when Spoolman material is ABS / ASA / PA / PC etc.') +
        _pcField(safe, 'printer_purchase_cost',  'Purchase cost ($)',            p.printer_purchase_cost, 1,   'What you paid. Used with lifespan to derive depreciation/hr.') +
        _pcField(safe, 'estimated_life_hrs',     'Lifespan (hours)',             p.estimated_life_hrs,    100, 'Expected print hours before replacement.') +
        _pcField(safe, 'depreciation_per_hr',    'Depreciation override ($/hr)', p.depreciation_per_hr,  0.001,'Direct $/hr override' + derivedDepreciation + '. 0 = derive from cost ÷ hours.') +

        '</div></div>';
}

function _pcField(safeName, field, label, value, step, hint) {
    var id = 'pc_' + safeName + '_' + field;
    return '<div>' +
        '<label style="font-size:0.8em;color:#aaa;display:block;margin-bottom:3px;">' + label + '</label>' +
        '<input type="number" id="' + id + '" min="0" step="' + step + '" value="' + (value || 0) + '" ' +
        'style="width:100%;box-sizing:border-box;padding:5px 8px;border-radius:4px;border:1px solid #444;background:rgba(255,255,255,0.05);color:#fff;font-size:0.9em;">' +
        (hint ? '<small style="color:#555;font-size:0.75em;">' + hint + '</small>' : '') +
        '</div>';
}

function savePrinterCostSettings(printerName) {
    var safe = printerName.replace(/[^a-zA-Z0-9_-]/g, '_');
    function v(field) {
        var el = document.getElementById('pc_' + safe + '_' + field);
        return el ? (parseFloat(el.value) || 0) : 0;
    }
    var payload = {
        printer_name:          printerName,
        print_wattage_w:       v('print_wattage_w'),
        preheat_wattage_w:     v('preheat_wattage_w'),
        preheat_time_min:      v('preheat_time_min'),
        high_temp_extra_w:     v('high_temp_extra_w'),
        printer_purchase_cost: v('printer_purchase_cost'),
        estimated_life_hrs:    v('estimated_life_hrs'),
        depreciation_per_hr:   v('depreciation_per_hr'),
    };

    // Use printer_name as the :id segment — resolvePrinterName on the server
    // falls back to treating it as a raw name when it's not a printer_configs ID.
    fetch('/api/printers/' + encodeURIComponent(printerName) + '/cost-settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
    })
        .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.json();
        })
        .then(function(data) {
            if (data.error) { showToast('Error: ' + data.error); return; }
            // Flash the save button, then reload cards from DB to confirm the write landed.
            // If the write silently failed the card will reset to 0, making the failure visible.
            var btns = document.querySelectorAll('#printerCostCards button');
            btns.forEach(function(b) {
                if (b.getAttribute('onclick') && b.getAttribute('onclick').includes(JSON.stringify(printerName))) {
                    var orig = b.textContent;
                    b.textContent = '✅ Saved!';
                    setTimeout(function() { b.textContent = orig; }, 1800);
                }
            });
            loadPrinterCostSettings();
        })
        .catch(function(err) { showToast('Error saving: ' + err.message); });
}

function _escHtml(s) {
    return String(s || '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function _fmtCurrencySimple(n) {
    return '$' + (Math.round(n * 1000) / 1000).toFixed(3);
}

// ─── Init ─────────────────────────────────────────────────────────────────────

// Load cost settings on page ready. Tab-visit reloads are handled by
// switchSettingsTab('cost') in main.js to avoid a race where the async
// response rebuilt the cards after the user had started typing.
document.addEventListener('DOMContentLoaded', function() {
    loadCostSettings();
    loadPrinterCostSettings();
});
