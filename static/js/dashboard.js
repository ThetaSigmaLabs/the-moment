// Dashboard functionality for The Moment

class Dashboard {
    constructor() {
        this.api = api;
        this.ws = null;
        this.printers = new Map();
        this.init();
    }

    async init() {
        await this.loadPrinters();
        this.setupWebSocket();
        this.setupEventListeners();
        this.startAutoRefresh();
    }

    async loadPrinters() {
        try {
            const printers = await this.api.getPrinters();
            printers.forEach(printer => {
                this.printers.set(printer.id, printer);
                this.renderPrinter(printer);
            });
        } catch (error) {
            console.error('Failed to load printers:', error);
            this.showError('Failed to load printer data');
        }
    }

    setupWebSocket() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsURL = `${protocol}//${window.location.host}/ws`;

        this.ws = new WebSocket(wsURL);

        this.ws.onmessage = (event) => {
            const data = JSON.parse(event.data);
            this.handleWebSocketMessage(data);
        };

        this.ws.onerror = (error) => {
            console.error('WebSocket error:', error);
        };

        this.ws.onclose = () => {
            console.log('WebSocket closed, reconnecting...');
            setTimeout(() => this.setupWebSocket(), 5000);
        };
    }


    handleWebSocketMessage(data) {
        switch (data.type) {
            case 'printer_status':
                this.updatePrinterStatus(data.printer_id, data.status);
                break;
            case 'print_started':
                this.handlePrintStarted(data);
                break;
            case 'print_completed':
                this.handlePrintCompleted(data);
                break;
            case 'spool_updated':
                this.handleSpoolUpdated(data);
                break;
            default:
                console.warn('Unknown WebSocket message type:', data.type);
        }
    }

    updatePrinterStatus(printerID, status) {
        const printer = this.printers.get(printerID);
        if (!printer) return;

        printer.status = status;
        this.renderPrinterStatus(printerID, status);
    }

    renderPrinterStatus(printerID, status) {
        const printerElement = document.querySelector(`[data-printer-id="${printerID}"]`);
        if (!printerElement) return;

        const statusElement = printerElement.querySelector('.status');
        if (statusElement) {
            const stateLabels = { 'ATTENTION': 'Printer Attention' };
            statusElement.textContent = stateLabels[status.state] || status.state;
            statusElement.className = `status ${status.state.toLowerCase()}`;
        }

        // Update temperature displays
        this.updateTemperatureDisplay(printerElement, status);
    }

    updateTemperatureDisplay(printerElement, status) {
        // Update bed temperature
        const bedTempElement = printerElement.querySelector('.bed-temp');
        if (bedTempElement && status.bed) {
            bedTempElement.textContent = `${status.bed.actual}°C / ${status.bed.target}°C`;
        }

        // Update tool temperatures
        if (status.tools) {
            status.tools.forEach((tool, index) => {
                const toolTempElement = printerElement.querySelector(`.tool-${index}-temp`);
                if (toolTempElement) {
                    toolTempElement.textContent = `${tool.actual}°C / ${tool.target}°C`;
                }
            });
        }
    }

    handlePrintStarted(data) {
        this.showNotification(`Print started on ${data.printer_name}: ${data.filename}`, 'info');
    }

    handlePrintCompleted(data) {
        this.showNotification(`Print completed on ${data.printer_name}`, 'success');
        this.refreshPrintHistory();
    }

    handleSpoolUpdated(data) {
        this.showNotification(`Spool ${data.spool_id} updated: ${data.remaining}g remaining`, 'info');
    }

    showNotification(message, type = 'info') {
        const notification = document.createElement('div');
        notification.className = `notification notification-${type}`;
        notification.textContent = message;
        document.body.appendChild(notification);

        setTimeout(() => {
            notification.classList.add('fade-out');
            setTimeout(() => notification.remove(), 300);
        }, 5000);
    }

    showError(message) {
        this.showNotification(message, 'error');
    }

    setupEventListeners() {
        // Add event listeners for UI interactions
        document.addEventListener('click', (e) => {
            if (e.target.matches('.refresh-printer')) {
                const printerID = e.target.dataset.printerId;
                this.refreshPrinter(printerID);
            }
        });
    }

    async refreshPrinter(printerID) {
        try {
            const status = await this.api.getPrinterStatus(printerID);
            this.updatePrinterStatus(printerID, status);
        } catch (error) {
            console.error(`Failed to refresh printer ${printerID}:`, error);
            this.showError(`Failed to refresh printer status`);
        }
    }

    startAutoRefresh() {
        // Refresh every 30 seconds
        setInterval(() => {
            this.printers.forEach((printer, printerID) => {
                this.refreshPrinter(printerID);
            });
        }, 30000);
    }

    async refreshPrintHistory() {
        // Reload print history section
        const historySection = document.getElementById('print-history');
        if (historySection) {
            try {
                const history = await this.api.getPrintHistory();
                this.renderPrintHistory(history);
            } catch (error) {
                console.error('Failed to refresh print history:', error);
            }
        }
    }

    renderPrintHistory(history) {
        const historySection = document.getElementById('print-history');
        if (!historySection) return;

        // Implementation for rendering print history
        // This would be called from the history tab
    }

    renderPrinter(printer) {
        // Implementation for rendering a printer card
        // This would be called during initial load
    }
}

// Initialize dashboard when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    window.dashboard = new Dashboard();
});
