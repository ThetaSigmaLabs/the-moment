// API client for The Moment backend

class TheMomentAPI {
    constructor(baseURL = '') {
        this.baseURL = baseURL;
    }

    async request(endpoint, options = {}) {
        const url = `${this.baseURL}${endpoint}`;
        const response = await fetch(url, {
            ...options,
            headers: {
                'Content-Type': 'application/json',
                ...options.headers,
            },
        });

        if (!response.ok) {
            throw new Error(`API request failed: ${response.statusText}`);
        }

        return response.json();
    }

    // Printer endpoints
    async getPrinters() {
        return this.request('/api/printers');
    }

    async getPrinterStatus(printerID) {
        return this.request(`/api/printers/${printerID}/status`);
    }

    // Spool endpoints
    async getSpools() {
        return this.request('/api/spools');
    }

    async getSpoolDetails(spoolID) {
        return this.request(`/api/spools/${spoolID}`);
    }

    // Print history endpoints
    async getPrintHistory(filters = {}) {
        const params = new URLSearchParams(filters);
        return this.request(`/api/history?${params}`);
    }

    async getPrintCost(printID) {
        return this.request(`/api/history/${printID}/cost`);
    }

    // Cost settings endpoints
    async getCostSettings() {
        return this.request('/api/settings/cost');
    }

    async updateCostSettings(settings) {
        return this.request('/api/settings/cost', {
            method: 'PUT',
            body: JSON.stringify(settings),
        });
    }

    // User endpoints
    async getCurrentUser() {
        return this.request('/api/user/current');
    }

    async getUsers() {
        return this.request('/api/users');
    }
}

// Export singleton instance
const api = new TheMomentAPI();