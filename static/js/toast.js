// Toast notification system — replaces browser alert() with non-blocking messages.
// Usage: showToast('message')  showToast('message', 'error')  showToast('message', 'success')
(function () {
    'use strict';

    var container;

    function ensureContainer() {
        if (container) return;
        container = document.createElement('div');
        container.id = 'toast-container';
        var s = container.style;
        s.position = 'fixed';
        s.bottom = '1.5rem';
        s.right = '1.5rem';
        s.zIndex = '9999';
        s.display = 'flex';
        s.flexDirection = 'column';
        s.gap = '0.5rem';
        s.maxWidth = '360px';
        s.pointerEvents = 'none';
        document.body.appendChild(container);
    }

    var COLORS = {
        info:    { bg: '#1e1a2e', border: 'rgba(124,92,252,0.35)' },
        success: { bg: '#064e3b', border: '#166534' },
        error:   { bg: '#7f1d1d', border: '#991b1b' },
        warning: { bg: '#3d1f00', border: '#92400e' }
    };

    window.showToast = function (message, type) {
        type = type || 'info';
        var colors = COLORS[type] || COLORS.info;

        ensureContainer();

        var toast = document.createElement('div');
        var ts = toast.style;
        ts.background = colors.bg;
        ts.border = '1px solid ' + colors.border;
        ts.color = '#f1f5f9';
        ts.padding = '0.625rem 0.875rem';
        ts.borderRadius = '0.375rem';
        ts.fontSize = '0.875rem';
        ts.lineHeight = '1.4';
        ts.boxShadow = '0 4px 12px rgba(0,0,0,0.4)';
        ts.opacity = '0';
        ts.transform = 'translateY(8px)';
        ts.transition = 'opacity 0.2s ease, transform 0.2s ease';
        ts.pointerEvents = 'auto';
        ts.cursor = 'pointer';
        ts.wordBreak = 'break-word';
        toast.textContent = message;
        toast.title = 'Click to dismiss';

        toast.addEventListener('click', function () { dismiss(toast); });

        container.appendChild(toast);

        // Trigger transition
        requestAnimationFrame(function () {
            requestAnimationFrame(function () {
                toast.style.opacity = '1';
                toast.style.transform = 'translateY(0)';
            });
        });

        var duration = type === 'error' ? 6000 : 4000;
        var timer = setTimeout(function () { dismiss(toast); }, duration);

        toast._dismissTimer = timer;
    };

    function dismiss(toast) {
        if (toast._dismissed) return;
        toast._dismissed = true;
        clearTimeout(toast._dismissTimer);
        toast.style.opacity = '0';
        toast.style.transform = 'translateY(8px)';
        setTimeout(function () {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        }, 220);
    }
}());
