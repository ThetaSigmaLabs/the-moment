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
            if (data.error) { alert('Error: ' + data.error); return; }
            var btn = document.querySelector('button[onclick="saveCostSettings()"]');
            if (btn) {
                var orig = btn.textContent;
                btn.textContent = '✅ Saved!';
                setTimeout(function() { btn.textContent = orig; }, 1800);
            }
        })
        .catch(function(err) { alert('Error saving: ' + err.message); });
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
               '<span>' + val + (dim ? ' <span style="color:#666;font-size:0.8em;">' + dim + '</span>' : '') + '</span>' +
               '</div>';
    };

    var html = '';
    if (d.filament_grams !== undefined) {
        html += row('Filament used', d.filament_grams.toFixed(2) + ' g');
    }
    if (d.filament_price_per_kg !== undefined && d.filament_price_per_kg > 0) {
        html += row('Filament cost', fmt(d.filament_cost),
                    '(' + fmt(d.filament_price_per_kg) + '/kg)');
    } else {
        html += row('Filament cost', fmt(d.filament_cost),
                    '(no price in Spoolman)');
    }
    if (d.print_time_min !== undefined && d.print_time_min > 0) {
        html += row('Print time', _fmtMin(d.print_time_min));
    }
    html += row('Electricity', fmt(d.electricity_cost));
    html += row('Maintenance', fmt(d.maintenance_cost));
    if (d.depreciation_cost !== undefined && d.depreciation_cost > 0) {
        html += row('Depreciation', fmt(d.depreciation_cost));
    }
    html += row('Subtotal', fmt(d.sub_total));
    if (d.margin_amount > 0) {
        var pct = d.settings ? d.settings.margin_percent : 0;
        html += row('Margin', fmt(d.margin_amount), '(' + pct + '%)');
    }

    html += '<div style="display:flex;justify-content:space-between;padding:8px 0;' +
            'border-top:2px solid #444;margin-top:4px;font-weight:700;font-size:1.05em;">' +
            '<span>Total</span><span style="color:#c8b8ff;">' + fmt(d.total_cost) + '</span></div>';

    if (!d.filament_price_per_kg || d.filament_price_per_kg === 0) {
        html += '<p style="color:#ffb74d;font-size:0.8em;margin-top:8px;">' +
                '⚠️ No price set for this spool in Spoolman. ' +
                'Add a price in Spoolman or use the override field above.</p>';
    }
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

// Load cost settings whenever the cost tab is opened
document.addEventListener('DOMContentLoaded', function() {
    // Hook into settings tab switching
    var costTabBtn = document.querySelector('button[onclick*="switchSettingsTab(\'cost\'"]') ||
                     document.querySelector('button[onclick*="cost"]');
    if (costTabBtn) {
        costTabBtn.addEventListener('click', function() {
            setTimeout(loadCostSettings, 50);
        });
    }
    // Also load on page ready in case cost tab is default
    loadCostSettings();
});
