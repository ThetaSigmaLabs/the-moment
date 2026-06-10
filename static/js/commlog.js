// Communication Log dialog — live view of TX/RX events between The Moment and a printer.
// Polls GET /api/printers/:id/comm-log?since=<id> every second while open.

(function () {
    var _printerID = null;
    var _lastID = -1;
    var _timer = null;
    var _atBottom = true;

    var DIR_LABEL = { TX: 'TX', RX: 'RX', EV: 'EV' };
    var DIR_CLASS = { TX: 'cl-tx', RX: 'cl-rx', EV: 'cl-ev', error: 'cl-error' };

    function getEntriesEl() { return document.getElementById('comm-log-entries'); }
    function getModal()     { return document.getElementById('comm-log-modal'); }

    // ── Public: open dialog ────────────────────────────────────────────────────
    window.openCommLog = function (printerID, printerName) {
        _printerID = printerID;
        _lastID = -1;
        _atBottom = true;

        var title = document.getElementById('comm-log-title');
        if (title) title.textContent = printerName || printerID;

        var el = getEntriesEl();
        if (el) el.innerHTML = '';

        var modal = getModal();
        if (modal) modal.style.display = 'flex';

        // Load initial batch then start live poll
        fetch('/api/printers/' + encodeURIComponent(printerID) + '/comm-log')
            .then(function (r) { return r.json(); })
            .then(function (data) {
                _appendEntries(data.entries || []);
                if (data.entries && data.entries.length > 0) {
                    _lastID = data.entries[data.entries.length - 1].id;
                }
            })
            .catch(function () {});

        if (_timer) clearInterval(_timer);
        _timer = setInterval(_poll, 1000);
    };

    // ── Public: close dialog ───────────────────────────────────────────────────
    window.closeCommLog = function () {
        var modal = getModal();
        if (modal) modal.style.display = 'none';
        _printerID = null;
        if (_timer) { clearInterval(_timer); _timer = null; }
    };

    // ── Public: select all ─────────────────────────────────────────────────────
    window.commLogSelectAll = function () {
        var el = getEntriesEl();
        if (!el) return;
        el.querySelectorAll('.cl-check').forEach(function (cb) { cb.checked = true; });
    };

    // ── Public: copy selected rows as plain text ───────────────────────────────
    window.commLogCopySelected = function () {
        var el = getEntriesEl();
        if (!el) return;
        var lines = [];
        el.querySelectorAll('.cl-row').forEach(function (row) {
            var cb = row.querySelector('.cl-check');
            if (!cb || !cb.checked) return;
            var time    = (row.querySelector('.cl-time')    || {}).textContent || '';
            var dir     = (row.querySelector('.cl-dir')     || {}).textContent || '';
            var evtype  = (row.querySelector('.cl-type')    || {}).textContent || '';
            var summary = (row.querySelector('.cl-summary') || {}).textContent || '';
            lines.push([time, dir, evtype, summary].join('\t'));
        });
        if (!lines.length) return;
        var text = lines.join('\n');
        if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text).catch(function () {
                window.prompt('Copy:', text);
            });
        } else {
            window.prompt('Copy:', text);
        }
    };

    // ── Public: clear entries (keeps polling) ──────────────────────────────────
    window.commLogClear = function () {
        var el = getEntriesEl();
        if (el) el.innerHTML = '';
        // Don't reset _lastID — we only want new events from here on
    };

    // ── Internal: poll for new entries ────────────────────────────────────────
    function _poll() {
        if (!_printerID) return;
        fetch('/api/printers/' + encodeURIComponent(_printerID) + '/comm-log?since=' + _lastID)
            .then(function (r) { return r.json(); })
            .then(function (data) {
                var entries = data.entries || [];
                if (!entries.length) return;
                _appendEntries(entries);
                _lastID = entries[entries.length - 1].id;
            })
            .catch(function () {});
    }

    // ── Internal: render and append rows ──────────────────────────────────────
    function _appendEntries(entries) {
        if (!entries || !entries.length) return;
        var el = getEntriesEl();
        if (!el) return;

        // Detect if user scrolled up before we add rows
        var scrolledToBottom = el.scrollHeight - el.scrollTop <= el.clientHeight + 4;

        var frag = document.createDocumentFragment();
        entries.forEach(function (e) {
            var dirClass = (e.type === 'error') ? DIR_CLASS.error : (DIR_CLASS[e.dir] || 'cl-ev');
            var row = document.createElement('div');
            row.className = 'cl-row ' + dirClass;
            row.dataset.id = e.id;

            var t = new Date(e.time);
            var timeStr = _padZ(t.getHours()) + ':' + _padZ(t.getMinutes()) + ':' + _padZ(t.getSeconds()) + '.' + _padZ3(t.getMilliseconds());

            row.innerHTML =
                '<input type="checkbox" class="cl-check">' +
                '<span class="cl-time">' + timeStr + '</span>' +
                '<span class="cl-dir">'  + _esc(DIR_LABEL[e.dir] || e.dir) + '</span>' +
                '<span class="cl-type">' + _esc(e.type) + '</span>' +
                '<span class="cl-summary">' + _esc(e.summary) + '</span>';

            if (e.detail) {
                var detail = document.createElement('div');
                detail.className = 'cl-detail';
                detail.textContent = e.detail;
                row.appendChild(detail);
            }
            frag.appendChild(row);
        });

        el.appendChild(frag);

        if (scrolledToBottom) {
            el.scrollTop = el.scrollHeight;
        }
    }

    function _padZ(n)  { return n < 10  ? '0' + n : '' + n; }
    function _padZ3(n) { return n < 10  ? '00' + n : n < 100 ? '0' + n : '' + n; }

    function _esc(s) {
        if (!s) return '';
        return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
    }

    // Close on backdrop click
    document.addEventListener('DOMContentLoaded', function () {
        var modal = getModal();
        if (modal) {
            modal.addEventListener('click', function (e) {
                if (e.target === modal) window.closeCommLog();
            });
        }
    });
})();
