// Cost calculation functionality for The Moment

class CostCalculator {
    constructor() {
        this.api = api;
        this.settings = null;
        this.init();
    }

    async init() {
        await this.loadSettings();
    }

    async loadSettings() {
        try {
            this.settings = await this.api.getCostSettings();
        } catch (error) {
            console.error('Failed to load cost settings:', error);
            // Use default settings
            this.settings = {
                electricity_rate: 0.12,
                printer_wattage: 250,
                maintenance_rate: 0.50,
                currency: 'USD',
                include_electricity: true,
                include_maintenance: true
            };
        }
    }

    calculateFilamentCost(weightGrams, costPerKg) {
        return (weightGrams / 1000) * costPerKg;
    }

    calculateElectricityCost(printTimeMinutes) {
        if (!this.settings.include_electricity) return 0;
        
        const printTimeHours = printTimeMinutes / 60;
        const kwhUsed = (this.settings.printer_wattage / 1000) * printTimeHours;
        return kwhUsed * this.settings.electricity_rate;
    }

    calculateMaintenanceCost(printTimeMinutes) {
        if (!this.settings.include_maintenance) return 0;
        
        const printTimeHours = printTimeMinutes / 60;
        return printTimeHours * this.settings.maintenance_rate;
    }

    calculateTotalCost(filamentCost, printTimeMinutes) {
        const electricityCost = this.calculateElectricityCost(printTimeMinutes);
        const maintenanceCost = this.calculateMaintenanceCost(printTimeMinutes);
        
        return {
            filament: filamentCost,
            electricity: electricityCost,
            maintenance: maintenanceCost,
            total: filamentCost + electricityCost + maintenanceCost,
            currency: this.settings.currency
        };
    }

    formatCost(amount) {
        return new Intl.NumberFormat('en-US', {
            style: 'currency',
            currency: this.settings.currency
        }).format(amount);
    }

    renderCostBreakdown(cost) {
        return `
            <div class="cost-breakdown">
                <div class="cost-item">
                    <span>Filament:</span>
                    <span>${this.formatCost(cost.filament)}</span>
                </div>
                ${cost.electricity > 0 ? `
                <div class="cost-item">
                    <span>Electricity:</span>
                    <span>${this.formatCost(cost.electricity)}</span>
                </div>
                ` : ''}
                ${cost.maintenance > 0 ? `
                <div class="cost-item">
                    <span>Maintenance:</span>
                    <span>${this.formatCost(cost.maintenance)}</span>
                </div>
                ` : ''}
                <div class="cost-item cost-total">
                    <span><strong>Total:</strong></span>
                    <span><strong>${this.formatCost(cost.total)}</strong></span>
                </div>
            </div>
        `;
    }
}

// Initialize cost calculator
window.costCalculator = new CostCalculator();