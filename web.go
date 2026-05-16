// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"sort"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/skip2/go-qrcode"
)

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/**
var staticFS embed.FS

// maskedCredential is returned in place of real credential values in API responses.
// On write, callers that receive this sentinel back must preserve the stored value.
const maskedCredential = "***"

// WebServer handles HTTP requests using Gin
type WebServer struct {
	bridge         *FilamentBridge
	router         *gin.Engine
	operationMutex sync.Mutex // Protects add/update/delete printer operations
	wsHub          *WebSocketHub
}

// WebSocketHub manages WebSocket connections and broadcasts
type WebSocketHub struct {
	clients    map[*WebSocketClient]bool
	register   chan *WebSocketClient
	unregister chan *WebSocketClient
	broadcast  chan []byte
	mutex      sync.RWMutex
}

// WebSocketClient represents a WebSocket connection
type WebSocketClient struct {
	hub  *WebSocketHub
	conn *websocket.Conn
	send chan []byte
}

// WebSocketMessage represents the structure of messages sent to clients
type WebSocketMessage struct {
	Type             string                             `json:"type"`
	Timestamp        time.Time                          `json:"timestamp"`
	Printers         map[string]PrinterData             `json:"printers"`
	Spools           []SpoolmanSpool                    `json:"spools"`
	ToolheadMappings map[string]map[int]ToolheadMapping `json:"toolhead_mappings"`
	PrintErrors      []PrintError                       `json:"print_errors,omitempty"`
}

// NewWebServer creates a new web server with Gin
func NewWebServer(bridge *FilamentBridge) *WebServer {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	// Add middleware
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// Add custom recovery middleware for API routes to ensure JSON responses
	router.Use(func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Check if this is an API route
				if strings.HasPrefix(c.Request.URL.Path, "/api/") {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
					c.Abort()
				} else {
					// For non-API routes, use default recovery behavior
					c.AbortWithStatus(http.StatusInternalServerError)
				}
			}
		}()
		c.Next()
	})

	// Create WebSocket hub
	wsHub := &WebSocketHub{
		clients:    make(map[*WebSocketClient]bool),
		register:   make(chan *WebSocketClient),
		unregister: make(chan *WebSocketClient),
		broadcast:  make(chan []byte),
	}

	ws := &WebServer{
		bridge: bridge,
		router: router,
		wsHub:  wsHub,
	}

	// Start WebSocket hub
	go wsHub.run()

	ws.setupRoutes()
	return ws
}

// generateToolheadIDs generates a slice of toolhead IDs from 0 to count-1
func generateToolheadIDs(count int) []int {
	ids := make([]int, count)
	for i := 0; i < count; i++ {
		ids[i] = i
	}
	return ids
}

// setupRoutes configures all the routes
func (ws *WebServer) setupRoutes() {
	// Load HTML templates with custom functions from embedded filesystem
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"generateToolheadIDs": generateToolheadIDs,
	}).ParseFS(templatesFS, "templates/*"))
	ws.router.SetHTMLTemplate(tmpl)

	// Static files (embedded in binary)
	// Use fs.Sub to strip the "static/" prefix from embedded paths
	staticSubFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("Failed to create static filesystem: %v", err)
	}
	ws.router.StaticFS("/static", http.FS(staticSubFS))

	// Main dashboard
	ws.router.GET("/", ws.dashboardHandler)

	// Browser pages for NFC spool-UUID (OpenPrintTag) workflow — mobile-friendly
	ws.router.GET("/nfc/spool/:uuid", ws.nfcSpoolScanHandler)
	ws.router.POST("/nfc/spool/:uuid/assign", ws.nfcSpoolAssignHandler)
	ws.router.GET("/nfc/spool/:uuid/displaced", ws.nfcSpoolDisplacedHandler)
	ws.router.POST("/nfc/spool/:uuid/complete", ws.nfcSpoolCompleteHandler)

	// API routes
	api := ws.router.Group("/api")
	{
		api.GET("/status", ws.statusHandler)
		api.GET("/spools", ws.spoolsHandler)
		api.GET("/filaments", ws.filamentsHandler)
		api.POST("/map_toolhead", ws.mapToolheadHandler)
		api.GET("/available_spools", ws.availableSpoolsHandler)
		api.GET("/spoolman/test", ws.testSpoolmanConnectionHandler)
		api.POST("/spoolman/test-url", ws.testSpoolmanURLHandler)
		api.GET("/spoolman/debug", ws.debugSpoolmanHandler)
		api.POST("/test/print_complete", ws.testPrintCompleteHandler)
		api.GET("/config", ws.getConfigHandler)
		api.POST("/config", ws.updateConfigHandler)
		api.GET("/config/auto-assign-previous-spool", ws.getAutoAssignPreviousSpoolHandler)
		api.PUT("/config/auto-assign-previous-spool", ws.updateAutoAssignPreviousSpoolHandler)
		api.GET("/printers", ws.getPrintersHandler)
		api.POST("/printers", ws.addPrinterHandler)
		api.PUT("/printers/:id", ws.updatePrinterHandler)
		api.DELETE("/printers/:id", ws.deletePrinterHandler)
		api.GET("/printers/:id/toolheads", ws.getToolheadNamesHandler)
		api.PUT("/printers/:id/toolheads/:toolhead_id", ws.updateToolheadNameHandler)
		api.POST("/detect_printer", ws.detectPrinterHandler)

		// Virtual test printer — file management
		api.POST("/printers/virtual", ws.addVirtualPrinterHandler)
		api.GET("/printers/:id/files", ws.listVirtualFilesHandler)
		api.POST("/printers/:id/files", ws.uploadVirtualFileHandler)
		api.DELETE("/printers/:id/files/:file_id", ws.deleteVirtualFileHandler)
		api.POST("/printers/:id/files/:file_id/process", ws.processVirtualFileHandler)
		api.GET("/printers/:id/files/:file_id/download", ws.downloadVirtualFileHandler)

		// Virtual printer export / import
		api.GET("/printers/:id/export", ws.exportVirtualPrinterHandler)
		api.POST("/printers/import", ws.importVirtualPrinterHandler)

		// Spool assignment maintenance
		api.GET("/orphaned-mappings", ws.getOrphanedMappingsHandler)
		api.DELETE("/orphaned-mappings", ws.clearOrphanedMappingsHandler)

		// Version metadata (no auth required)
		api.GET("/version", ws.versionHandler)

		// OctoPrint push endpoint and diagnostics
		api.POST("/prints", ws.receivePrintHandler)
		api.GET("/octoprint/ping", ws.octoprintPingHandler)

		// Print history and sessions
		api.GET("/sessions", ws.getSessionsHandler)
		api.GET("/history", ws.getHistoryHandler)
		api.GET("/history/:id", ws.getHistoryEntryHandler)
		api.PATCH("/history/:id/note", ws.updateHistoryNoteHandler)
		api.DELETE("/history/:id", ws.deleteHistoryEntryHandler)
		api.GET("/history/:id/tags", ws.getHistoryTagsHandler)
		api.POST("/history/:id/tags", ws.setHistoryTagsHandler)

		// Cost settings and calculation
		api.GET("/cost-settings", ws.getCostSettingsHandler)
		api.POST("/cost-settings", ws.setCostSettingsHandler)
		api.GET("/cost-settings/printers", ws.getAllPrinterCostSettingsHandler)
		api.GET("/printers/:id/cost-settings", ws.getPrinterCostSettingsHandler)
		api.POST("/printers/:id/cost-settings", ws.setPrinterCostSettingsHandler)
		api.POST("/cost/calculate", ws.calculateCostHandler)
		api.GET("/print-errors", ws.getPrintErrorsHandler)
		api.POST("/print-errors/:id/acknowledge", ws.acknowledgePrintErrorHandler)
		api.GET("/nfc/assign", ws.nfcAssignHandler)
		api.GET("/nfc/urls", ws.nfcUrlsHandler)
		api.GET("/nfc/session/status", ws.nfcSessionStatusHandler)

		// NFC tag management (spool UUID / OpenPrintTag workflow)
		api.POST("/nfc/spool/:id/tag", ws.nfcAssignTagHandler)
		api.DELETE("/nfc/spool/:id/tag", ws.nfcRemoveTagHandler)
		api.GET("/nfc/config", ws.nfcConfigHandler)
		api.POST("/nfc/config", ws.nfcSaveConfigHandler)

		// Post-print spool segment reassignment
		api.POST("/prints/:id/filament/:segment_id/reassign", ws.reassignFilamentHandler)
		api.GET("/locations", ws.getLocationsHandler)
		api.GET("/locations/:name/status", ws.getLocationStatusHandler)
		api.POST("/locations", ws.createLocationHandler)
		api.PUT("/locations/:name", ws.updateLocationHandler)
		api.DELETE("/locations/:name", ws.deleteLocationHandler)
	}

	// WebSocket endpoint
	ws.router.GET("/ws/status", ws.websocketHandler)
}

// WebSocket hub methods

// run starts the WebSocket hub
func (h *WebSocketHub) run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()
			log.Printf("WebSocket client connected. Total clients: %d", len(h.clients))

		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mutex.Unlock()
			log.Printf("WebSocket client disconnected. Total clients: %d", len(h.clients))

		case message := <-h.broadcast:
			h.mutex.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mutex.RUnlock()
		}
	}
}

// BroadcastStatus sends status updates to all connected clients
func (ws *WebServer) BroadcastStatus() {
	// Get current status
	status, err := ws.bridge.GetStatus()
	if err != nil {
		log.Printf("Error getting status for broadcast: %v", err)
		return
	}

	// Get current spools
	spools, err := ws.bridge.spoolman.GetAllSpools()
	if err != nil {
		log.Printf("Error getting spools for broadcast: %v", err)
		spools = []SpoolmanSpool{}
	}

	// Get print errors
	printErrors := ws.bridge.GetPrintErrors()

	// Create message
	message := WebSocketMessage{
		Type:             "status_update",
		Timestamp:        time.Now(),
		Printers:         status.Printers,
		Spools:           spools,
		ToolheadMappings: status.ToolheadMappings,
		PrintErrors:      printErrors,
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling WebSocket message: %v", err)
		return
	}

	// Broadcast to all clients
	select {
	case ws.wsHub.broadcast <- jsonData:
		log.Printf("Broadcasted status update to %d clients", len(ws.wsHub.clients))
	default:
		log.Printf("No clients connected to receive broadcast")
	}
}

// websocketHandler handles WebSocket connections
func (ws *WebServer) websocketHandler(c *gin.Context) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow connections from any origin
		},
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &WebSocketClient{
		hub:  ws.wsHub,
		conn: conn,
		send: make(chan []byte, 256),
	}

	client.hub.register <- client

	// Start goroutines for reading and writing
	go client.writePump()
	go client.readPump()
}

// WebSocket client methods

// readPump pumps messages from the WebSocket connection to the hub
func (c *WebSocketClient) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
	}
}

// writePump pumps messages from the hub to the WebSocket connection
func (c *WebSocketClient) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued chat messages to the current websocket message
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// dashboardHandler serves the main dashboard
func (ws *WebServer) dashboardHandler(c *gin.Context) {
	status, err := ws.bridge.GetStatus()
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"Error": "Failed to get printer status",
		})
		return
	}

	// Test Spoolman connection
	spoolmanConnected := true
	spoolmanError := ""
	spools, err := ws.bridge.spoolman.GetAllSpools()
	if err != nil {
		spoolmanConnected = false
		spoolmanError = err.Error()
		spools = []SpoolmanSpool{}
	}

	// Check if this is a first run
	isFirstRun, err := ws.bridge.IsFirstRun()
	if err != nil {
		isFirstRun = false
	}

	hasErrors := !spoolmanConnected || hasConnectionErrors(status)

	// Get print errors
	printErrors := ws.bridge.GetPrintErrors()
	hasPrintErrors := len(printErrors) > 0

	c.HTML(http.StatusOK, "index.html", gin.H{
		"Status":            status,
		"Spools":            spools,
		"HasErrors":         hasErrors,
		"HasPrintErrors":    hasPrintErrors,
		"PrintErrors":       printErrors,
		"IsFirstRun":        isFirstRun,
		"Printers":          ws.bridge.config.Printers,
		"SpoolmanConnected": spoolmanConnected,
		"SpoolmanError":     spoolmanError,
		"SpoolmanBaseURL":   ws.bridge.config.SpoolmanURL,
		"AppVersion":        AppVersion,
	})
}

// hasConnectionErrors checks if there are connection errors
func hasConnectionErrors(status *PrinterStatus) bool {
	for _, printer := range status.Printers {
		if printer.State == StateOffline {
			return true
		}
	}
	return false
}

// statusHandler returns current status as JSON
func (ws *WebServer) statusHandler(c *gin.Context) {
	status, err := ws.bridge.GetStatus()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

// spoolsHandler returns all spools as JSON
func (ws *WebServer) spoolsHandler(c *gin.Context) {
	spools, err := ws.bridge.spoolman.GetAllSpools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, spools)
}

// filamentsHandler returns all filament types as JSON
func (ws *WebServer) filamentsHandler(c *gin.Context) {
	filaments, err := ws.bridge.spoolman.GetAllFilaments()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, filaments)
}

// validatePrinterConfig validates printer configuration input
func validatePrinterConfig(config PrinterConfig) error {
	if config.Name == "" {
		return fmt.Errorf("printer name is required")
	}
	// Virtual printers use sentinel IP "virtual" — no real address required
	if !config.IsVirtual && config.IPAddress == "" {
		return fmt.Errorf("address is required")
	}
	if config.Toolheads < 1 {
		return fmt.Errorf("toolheads must be at least 1")
	}
	maxHeads := 10
	if config.IsVirtual {
		maxHeads = MaxToolheads // up to 16 for INDX simulation
	}
	if config.Toolheads > maxHeads {
		return fmt.Errorf("toolheads cannot exceed %d", maxHeads)
	}
	return nil
}

// validateAddress validates hostname or IP address format
func validateAddress(address string) error {
	if address == "" {
		return fmt.Errorf("address cannot be empty")
	}
	// Basic validation - check for reasonable length (hostnames can be longer than IPs)
	// Minimum: 1 character (e.g., "a"), Maximum: 253 characters (RFC 1035)
	if len(address) < 1 || len(address) > 253 {
		return fmt.Errorf("invalid address format")
	}
	// Basic character validation - allow common characters used in hostnames and IP addresses
	// This includes: letters, numbers, dots, hyphens, underscores, colons (for IPv6), and brackets (for IPv6)
	// The HTTP client will perform more thorough validation when connecting
	for _, char := range address {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '-' || char == '_' ||
			char == ':' || char == '[' || char == ']') {
			return fmt.Errorf("invalid address format: contains invalid characters")
		}
	}
	return nil
}

// mapToolheadHandler maps a spool to a toolhead
func (ws *WebServer) mapToolheadHandler(c *gin.Context) {
	var req struct {
		PrinterName string `json:"printer_name" binding:"required"`
		ToolheadID  int    `json:"toolhead_id"`
		SpoolID     int    `json:"spool_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
		return
	}

	if req.PrinterName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing required parameters"})
		return
	}

	if req.ToolheadID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Toolhead ID must be non-negative"})
		return
	}

	// Handle unmapping (SpoolID = 0) or mapping (SpoolID > 0)
	if req.SpoolID == 0 {
		// Unmap the toolhead
		if err := ws.bridge.UnmapToolhead(req.PrinterName, req.ToolheadID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Toolhead unmapped successfully"})
	} else {
		// Map the spool to the toolhead
		if err := ws.bridge.SetToolheadMapping(req.PrinterName, req.ToolheadID, req.SpoolID); err != nil {
			// Check if this is a spool conflict error
			if strings.Contains(err.Error(), "is already assigned to") {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Toolhead mapped successfully"})
	}
}

// availableSpoolsHandler returns spools available for assignment to a specific toolhead
func (ws *WebServer) availableSpoolsHandler(c *gin.Context) {
	printerName := c.Query("printer_name")
	toolheadIDStr := c.Query("toolhead_id")

	if printerName == "" || toolheadIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "printer_name and toolhead_id parameters are required"})
		return
	}

	toolheadID, err := strconv.Atoi(toolheadIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid toolhead_id"})
		return
	}

	// Get all spools from Spoolman
	allSpools, err := ws.bridge.spoolman.GetAllSpools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get all current toolhead mappings
	allMappings, err := ws.bridge.GetAllToolheadMappings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Create a set of assigned spool IDs (excluding the current toolhead)
	assignedSpoolIDs := make(map[int]bool)
	for _, printerMappings := range allMappings {
		for tid, mapping := range printerMappings {
			// Skip the current toolhead (allow re-assignment to the same toolhead)
			if mapping.PrinterName == printerName && tid == toolheadID {
				continue
			}
			// Mark this spool as assigned (prevents same spool being used on multiple printers)
			assignedSpoolIDs[mapping.SpoolID] = true
		}
	}

	// Filter out assigned spools
	var availableSpools []SpoolmanSpool
	for _, spool := range allSpools {
		if !assignedSpoolIDs[spool.ID] {
			availableSpools = append(availableSpools, spool)
		}
	}

	c.JSON(http.StatusOK, gin.H{"spools": availableSpools})
}

// getConfigHandler returns current configuration
func (ws *WebServer) getConfigHandler(c *gin.Context) {
	config, err := ws.bridge.GetAllConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if config[ConfigKeyTheMomentAPIKey] != "" {
		config[ConfigKeyTheMomentAPIKey] = maskedCredential
	}
	c.JSON(http.StatusOK, config)
}

// updateConfigHandler updates configuration
func (ws *WebServer) updateConfigHandler(c *gin.Context) {
	var config map[string]string
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
		return
	}

	// Update each config value, skipping credential sentinels (unchanged masked values)
	for key, value := range config {
		if value == maskedCredential && key == ConfigKeyTheMomentAPIKey {
			continue
		}
		if err := ws.bridge.SetConfigValue(key, value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	// Reload configuration
	newConfig, err := LoadConfig(ws.bridge)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := ws.bridge.UpdateConfig(newConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Configuration updated successfully"})
}

// getAutoAssignPreviousSpoolHandler returns current auto-assign previous spool settings
func (ws *WebServer) getAutoAssignPreviousSpoolHandler(c *gin.Context) {
	enabled, err := ws.bridge.GetAutoAssignPreviousSpoolEnabled()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	location, err := ws.bridge.GetAutoAssignPreviousSpoolLocation()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"enabled":  enabled,
		"location": location,
	})
}

// updateAutoAssignPreviousSpoolHandler updates auto-assign previous spool settings
func (ws *WebServer) updateAutoAssignPreviousSpoolHandler(c *gin.Context) {
	var req struct {
		Enabled  bool   `json:"enabled" binding:"required"`
		Location string `json:"location"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON or missing 'enabled' field"})
		return
	}

	// Update enabled setting
	if err := ws.bridge.SetAutoAssignPreviousSpoolEnabled(req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Update location setting
	if err := ws.bridge.SetAutoAssignPreviousSpoolLocation(req.Location); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Auto-assign previous spool settings updated successfully"})
}

// getPrintersHandler returns all configured printers
func (ws *WebServer) getPrintersHandler(c *gin.Context) {
	printerConfigs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Enhance printer configs with toolhead names
	result := make(map[string]interface{})
	for printerID, printerConfig := range printerConfigs {
		maskedKey := ""
		if printerConfig.APIKey != "" {
			maskedKey = maskedCredential
		}
		printerType := printerConfig.PrinterType
		if printerType == "" {
			printerType = PrinterTypePrusaLink
		}
		printerData := map[string]interface{}{
			"name":         printerConfig.Name,
			"model":        printerConfig.Model,
			"ip_address":   printerConfig.IPAddress,
			"api_key":      maskedKey,
			"toolheads":    printerConfig.Toolheads,
			"is_virtual":   printerConfig.IsVirtual,
			"printer_type": printerType,
		}

		// Include uploaded file list for virtual printers so the card renders immediately
		if printerConfig.IsVirtual {
			files, _ := ws.bridge.GetVirtualPrinterFiles(printerID)
			printerData["files"] = files
		}

		// Get toolhead names for this printer
		toolheadNames, err := ws.bridge.GetAllToolheadNames(printerID)
		if err == nil {
			// Build toolhead names map with defaults
			toolheadNamesMap := make(map[int]string)
			for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
				if name, exists := toolheadNames[toolheadID]; exists {
					toolheadNamesMap[toolheadID] = name
				} else {
					toolheadNamesMap[toolheadID] = fmt.Sprintf("Toolhead %d", toolheadID)
				}
			}
			printerData["toolhead_names"] = toolheadNamesMap
		}

		result[printerID] = printerData
	}

	c.JSON(http.StatusOK, gin.H{"printers": result})
}

// addPrinterHandler adds a new printer configuration
func (ws *WebServer) addPrinterHandler(c *gin.Context) {
	// Serialize printer operations to prevent race conditions
	ws.operationMutex.Lock()
	defer ws.operationMutex.Unlock()

	var printerConfig PrinterConfig
	if err := c.ShouldBindJSON(&printerConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate printer configuration
	if err := validatePrinterConfig(printerConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate address
	if err := validateAddress(printerConfig.IPAddress); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Generate a unique printer ID using nanosecond timestamp + random component
	printerID := fmt.Sprintf("printer_%d_%d", time.Now().UnixNano(), time.Now().Nanosecond()%1000)

	// Save the printer configuration
	if err := ws.bridge.SavePrinterConfig(printerID, printerConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Reload configuration to include the new printer
	if err := ws.reloadBridgeConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload configuration"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Printer added successfully", "printer_id": printerID})
}

// updatePrinterHandler updates an existing printer configuration
func (ws *WebServer) updatePrinterHandler(c *gin.Context) {
	// Serialize printer operations to prevent race conditions
	ws.operationMutex.Lock()
	defer ws.operationMutex.Unlock()

	printerID := c.Param("id")

	var printerConfig PrinterConfig
	if err := c.ShouldBindJSON(&printerConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// If the client echoed back the masked sentinel, preserve the stored API key
	if printerConfig.APIKey == maskedCredential {
		existing, err := ws.bridge.GetAllPrinterConfigs()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if existingPrinter, ok := existing[printerID]; ok {
			printerConfig.APIKey = existingPrinter.APIKey
		} else {
			printerConfig.APIKey = ""
		}
	}

	// Validate printer configuration
	if err := validatePrinterConfig(printerConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate address
	if err := validateAddress(printerConfig.IPAddress); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Auto-detect model if address or API key changed, or if model is currently "Unknown"
	if printerConfig.Model == "" || printerConfig.Model == ModelUnknown {
		log.Printf("🔍 [Auto-Detection] Detecting model for printer %s (IP: %s)", printerID, printerConfig.IPAddress)

		// Create PrusaLink client for detection
		client := NewPrusaLinkClient(printerConfig.IPAddress, printerConfig.APIKey, 10, 60) // Use default timeouts for detection

		// Try to get printer info
		printerInfo, err := client.GetPrinterInfo()
		if err != nil {
			log.Printf("⚠️ [Auto-Detection] Failed to detect model for %s: %v (keeping current model: %s)",
				printerConfig.IPAddress, err, printerConfig.Model)
		} else {
			// Use shared model detection function
			detectedModel := detectPrinterModel(printerInfo.Hostname)

			if detectedModel != ModelUnknown {
				log.Printf("✅ [Auto-Detection] Detected model for %s: '%s' -> %s",
					printerConfig.IPAddress, printerInfo.Hostname, detectedModel)
				printerConfig.Model = detectedModel
			} else {
				log.Printf("❌ [Auto-Detection] No pattern matched for hostname '%s' from %s",
					printerInfo.Hostname, printerConfig.IPAddress)
			}
		}
	}

	// Save the updated printer configuration
	if err := ws.bridge.SavePrinterConfig(printerID, printerConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Reload configuration to include the updated printer
	if err := ws.reloadBridgeConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload configuration"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Printer updated successfully"})
}

// deletePrinterHandler deletes a printer configuration
func (ws *WebServer) deletePrinterHandler(c *gin.Context) {
	// Serialize printer operations to prevent race conditions
	ws.operationMutex.Lock()
	defer ws.operationMutex.Unlock()

	printerID := c.Param("id")

	// Delete the printer configuration
	if err := ws.bridge.DeletePrinterConfig(printerID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Reload configuration to remove the deleted printer
	if err := ws.reloadBridgeConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload configuration"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Printer deleted successfully"})
}

// getToolheadNamesHandler returns all toolhead names for a printer
func (ws *WebServer) getToolheadNamesHandler(c *gin.Context) {
	printerID := c.Param("id")

	// Verify printer exists
	printerConfigs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	printerConfig, exists := printerConfigs[printerID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	}

	// Get all toolhead names
	toolheadNames, err := ws.bridge.GetAllToolheadNames(printerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build response with all toolheads (including defaults for unnamed ones)
	result := make(map[int]string)
	for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
		if name, exists := toolheadNames[toolheadID]; exists {
			result[toolheadID] = name
		} else {
			result[toolheadID] = fmt.Sprintf("Toolhead %d", toolheadID)
		}
	}

	c.JSON(http.StatusOK, gin.H{"toolhead_names": result})
}

// updateToolheadNameHandler updates a toolhead's display name
func (ws *WebServer) updateToolheadNameHandler(c *gin.Context) {
	printerID := c.Param("id")
	toolheadIDStr := c.Param("toolhead_id")

	// Parse toolhead ID
	toolheadID, err := strconv.Atoi(toolheadIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid toolhead ID"})
		return
	}

	// Verify printer exists
	printerConfigs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	printerConfig, exists := printerConfigs[printerID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	}

	// Validate toolhead ID is within range
	if toolheadID < 0 || toolheadID >= printerConfig.Toolheads {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Toolhead ID must be between 0 and %d", printerConfig.Toolheads-1)})
		return
	}

	// Parse request body
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON or missing 'name' field"})
		return
	}

	// Update toolhead name
	if err := ws.bridge.SetToolheadName(printerID, toolheadID, req.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Toolhead name updated successfully"})
}

// detectPrinterModel detects printer model from hostname
func detectPrinterModel(hostname string) string {
	model := ModelUnknown
	hostnameLower := strings.ToLower(hostname)
	hostnameLower = strings.TrimSpace(hostnameLower) // Clean up any whitespace

	log.Printf("🔍 [Detection] Checking hostname '%s' against patterns:", hostnameLower)

	if strings.Contains(hostnameLower, ModelCorePattern) {
		model = ModelCoreOne
		log.Printf("✅ [Detection] Matched pattern '%s' -> %s", ModelCorePattern, model)
	} else if strings.Contains(hostnameLower, ModelXLPattern) {
		model = ModelXL
		log.Printf("✅ [Detection] Matched pattern '%s' -> %s", ModelXLPattern, model)
	} else if strings.Contains(hostnameLower, ModelMK4Pattern) {
		model = ModelMK4
		log.Printf("✅ [Detection] Matched pattern '%s' -> %s", ModelMK4Pattern, model)
	} else if strings.Contains(hostnameLower, ModelMK3Pattern) {
		model = ModelMK35
		log.Printf("✅ [Detection] Matched pattern '%s' -> %s", ModelMK3Pattern, model)
	} else if strings.Contains(hostnameLower, ModelMiniPattern) {
		model = ModelMiniPlus
		log.Printf("✅ [Detection] Matched pattern '%s' -> %s", ModelMiniPattern, model)
	} else {
		log.Printf("❌ [Detection] No pattern matched for hostname '%s'. Available patterns: %s, %s, %s, %s, %s",
			hostnameLower, ModelCorePattern, ModelXLPattern, ModelMK4Pattern, ModelMK3Pattern, ModelMiniPattern)
	}

	log.Printf("🎯 [Detection] Final result: hostname='%s' -> model='%s'", hostname, model)
	return model
}

// detectPrinterHandler detects printer model from PrusaLink API
func (ws *WebServer) detectPrinterHandler(c *gin.Context) {
	var req struct {
		IPAddress string `json:"ip_address" binding:"required"`
		APIKey    string `json:"api_key" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
		return
	}

	// Validate address
	if err := validateAddress(req.IPAddress); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("🔍 [Detection] Starting printer model detection for IP: %s", req.IPAddress)

	// Create PrusaLink client
	client := NewPrusaLinkClient(req.IPAddress, req.APIKey, 10, 60) // Use default timeouts for detection

	// Try to get printer info, but don't fail if it times out
	printerInfo, err := client.GetPrinterInfo()
	if err != nil {
		log.Printf("❌ [Detection] Failed to get printer info from %s: %v", req.IPAddress, err)
		// If API call fails, return default values instead of error
		// This allows users to add printers even if they're offline
		c.JSON(http.StatusOK, gin.H{
			"model":    ModelUnknown,
			"hostname": "Unknown",
			"detected": false,
			"warning":  "Could not connect to printer. You can still add it manually.",
		})
		return
	}

	log.Printf("📥 [Detection] Received printer info: hostname='%s'", printerInfo.Hostname)

	// Use shared model detection function
	model := detectPrinterModel(printerInfo.Hostname)

	// Return detected information (toolheads will be provided by user)
	c.JSON(http.StatusOK, gin.H{
		"model":    model,
		"hostname": printerInfo.Hostname,
		"detected": true,
	})
}

// testSpoolmanConnectionHandler tests the connection to Spoolman
func (ws *WebServer) testSpoolmanConnectionHandler(c *gin.Context) {
	if err := ws.bridge.spoolman.TestConnection(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "connected": false})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Connection successful", "connected": true})
}

// testSpoolmanURLHandler tests a Spoolman connection using the provided URL (before saving)
func (ws *WebServer) testSpoolmanURLHandler(c *gin.Context) {
	var req struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "connected": false})
		return
	}
	if req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required", "connected": false})
		return
	}
	client := NewSpoolmanClient(req.URL, 10)
	if err := client.TestConnection(); err != nil {
		c.JSON(http.StatusOK, gin.H{"error": err.Error(), "connected": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Connection successful", "connected": true})
}

// debugSpoolmanHandler provides detailed debug information about Spoolman data
func (ws *WebServer) debugSpoolmanHandler(c *gin.Context) {
	spools, err := ws.bridge.spoolman.GetAllSpools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	debugInfo := gin.H{
		"spool_count": len(spools),
		"spools":      spools,
		"raw_data":    make([]gin.H, len(spools)),
	}

	// Add raw field analysis
	for i, spool := range spools {
		debugInfo["raw_data"].([]gin.H)[i] = gin.H{
			"id":               spool.ID,
			"name":             spool.Name,
			"brand":            spool.Brand,
			"material":         spool.Material,
			"color":            spool.Filament.ColorHex,
			"remaining_length": spool.RemainingLength,
			"name_empty":       spool.Name == "",
			"brand_empty":      spool.Brand == "",
			"material_empty":   spool.Material == "",
			"color_empty":      spool.Filament.ColorHex == "",
		}
	}

	c.JSON(http.StatusOK, debugInfo)
}

// testPrintCompleteHandler simulates a print completion for testing
func (ws *WebServer) testPrintCompleteHandler(c *gin.Context) {
	var request struct {
		PrinterName   string          `json:"printer_name" binding:"required"`
		JobName       string          `json:"job_name"`
		FilamentUsage map[int]float64 `json:"filament_usage"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if request.JobName == "" {
		request.JobName = "Test Print Job"
	}

	// If no filament usage provided, use default test values
	if len(request.FilamentUsage) == 0 {
		request.FilamentUsage = map[int]float64{
			0: 10.0, // 10g for toolhead 0
		}
	}

	// Get printer config - first try by name, then by ID
	var config PrinterConfig
	var found bool

	// Try to find by name first
	for _, printerConfig := range ws.bridge.config.Printers {
		if printerConfig.Name == request.PrinterName {
			config = printerConfig
			found = true
			break
		}
	}

	// If not found by name, try by ID
	if !found {
		config, found = ws.bridge.config.Printers[request.PrinterName]
	}

	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	}

	// Simulate the print completion with provided filament usage
	printerName := resolvePrinterName(config)

	// Process filament usage using helper function
	if err := ws.bridge.processFilamentUsage(printerName, request.FilamentUsage, request.JobName); err != nil {
		log.Printf("Error processing filament usage: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"message":        "Print completion simulated successfully",
		"printer":        request.PrinterName,
		"job":            request.JobName,
		"filament_usage": request.FilamentUsage,
	})
}

// ─── Virtual Test Printer Handlers ───────────────────────────────────────────

// addVirtualPrinterHandler creates a virtual test printer (no IP or API key needed).
func (ws *WebServer) addVirtualPrinterHandler(c *gin.Context) {
	ws.operationMutex.Lock()
	defer ws.operationMutex.Unlock()

	var req struct {
		Name      string `json:"name" binding:"required"`
		Toolheads int    `json:"toolheads"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Toolheads < 1 {
		req.Toolheads = 1
	}
	if req.Toolheads > MaxToolheads {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("toolheads cannot exceed %d", MaxToolheads)})
		return
	}

	printerID := fmt.Sprintf("virtual_%d", time.Now().UnixNano())
	cfg := PrinterConfig{
		Name:      req.Name,
		Model:     "Virtual Test Printer",
		IPAddress: "virtual",
		Toolheads: req.Toolheads,
		IsVirtual: true,
	}
	if err := ws.bridge.SavePrinterConfig(printerID, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := ws.reloadBridgeConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload configuration"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Virtual test printer created", "printer_id": printerID})
}

// uploadVirtualFileHandler accepts multipart .gcode or .bgcode uploads.
func (ws *WebServer) uploadVirtualFileHandler(c *gin.Context) {
	printerID := c.Param("id")

	configs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	printer, ok := configs[printerID]
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	}
	if !printer.IsVirtual {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File upload is only supported for virtual test printers"})
		return
	}

	if err := c.Request.ParseMultipartForm(100 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse upload"})
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file provided (use field name 'file')"})
		return
	}
	defer file.Close()

	lower := strings.ToLower(header.Filename)
	if !strings.HasSuffix(lower, ".gcode") && !strings.HasSuffix(lower, ".bgcode") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only .gcode and .bgcode files are supported"})
		return
	}

	content, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file"})
		return
	}

	client := &PrusaLinkClient{}
	usage, _ := client.ParseGcodeFilamentUsage(content)
	hasUsage := len(usage) > 0

	fileID, err := ws.bridge.SaveVirtualPrinterFile(printerID, header.Filename, header.Filename, content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	msg := "File uploaded successfully"
	if !hasUsage {
		msg = "File uploaded but no filament usage metadata found — Spoolman will not be updated when processed"
	}
	c.JSON(http.StatusOK, gin.H{
		"message":    msg,
		"file_id":    fileID,
		"filename":   header.Filename,
		"size_bytes": len(content),
		"has_usage":  hasUsage,
		"usage":      usage,
	})
}

// listVirtualFilesHandler returns metadata for all uploaded files on a virtual printer.
func (ws *WebServer) listVirtualFilesHandler(c *gin.Context) {
	printerID := c.Param("id")

	configs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if p, ok := configs[printerID]; !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	} else if !p.IsVirtual {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File listing is only available for virtual printers"})
		return
	}

	files, err := ws.bridge.GetVirtualPrinterFiles(printerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": files})
}

// deleteVirtualFileHandler deletes one uploaded G-code file.
func (ws *WebServer) deleteVirtualFileHandler(c *gin.Context) {
	printerID := c.Param("id")
	fileID, err := strconv.Atoi(c.Param("file_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file ID"})
		return
	}
	if err := ws.bridge.DeleteVirtualPrinterFile(printerID, fileID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "File deleted"})
}

// processVirtualFileHandler parses the G-code, updates Spoolman, and returns a
// per-toolhead breakdown plus a warning if any toolhead had usage but no spool mapped.
func (ws *WebServer) processVirtualFileHandler(c *gin.Context) {
	printerID := c.Param("id")
	fileID, err := strconv.Atoi(c.Param("file_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file ID"})
		return
	}

	usage, skipped, printTimeMin, err := ws.bridge.ProcessVirtualFile(printerID, fileID)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	total := 0.0
	for _, g := range usage {
		total += g
	}

	c.JSON(http.StatusOK, gin.H{
		"message":           "Filament usage processed and Spoolman updated",
		"usage":             usage,
		"total_g":           total,
		"toolheads":         len(usage),
		"print_time_min":    printTimeMin,
		"skipped_toolheads": skipped,
	})
}

// downloadVirtualFileHandler streams a stored G-code file back to the browser.
func (ws *WebServer) downloadVirtualFileHandler(c *gin.Context) {
	printerID := c.Param("id")
	fileID, err := strconv.Atoi(c.Param("file_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file ID"})
		return
	}

	content, displayName, err := ws.bridge.GetVirtualPrinterFileContent(fileID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Confirm ownership — prevent cross-printer file access
	files, _ := ws.bridge.GetVirtualPrinterFiles(printerID)
	for _, f := range files {
		if f.ID == fileID {
			contentType := "text/plain"
			if strings.HasSuffix(strings.ToLower(displayName), ".bgcode") {
				contentType = "application/octet-stream"
			}
			c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", displayName))
			c.Data(http.StatusOK, contentType, content)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "File not found for this printer"})
}

// ─── Virtual Printer Export / Import ─────────────────────────────────────────

// VirtualPrinterExport is the complete, portable snapshot of a virtual printer.
// It contains everything needed to recreate the printer on another instance,
// including uploaded G-code files (base64-encoded) and spool mappings.
// Spool IDs reference the target Spoolman instance — the user must ensure those
// IDs exist before importing.
type VirtualPrinterExport struct {
	ExportVersion int                      `json:"export_version"` // schema version for forward compat
	ExportedAt    string                   `json:"exported_at"`
	Printer       VirtualPrinterExportMeta `json:"printer"`
	ToolheadNames map[int]string           `json:"toolhead_names"`           // toolhead_id → display name
	SpoolMappings map[int]int              `json:"spool_mappings"`           // toolhead_id → spool_id
	Files         []VirtualPrinterFileExport `json:"files"`
}

// VirtualPrinterExportMeta is the printer config portion of the export.
type VirtualPrinterExportMeta struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Toolheads int    `json:"toolheads"`
}

// VirtualPrinterFileExport is one uploaded G-code file, content base64-encoded.
type VirtualPrinterFileExport struct {
	Filename    string `json:"filename"`
	DisplayName string `json:"display_name"`
	FileSize    int64  `json:"file_size"`
	UploadedAt  string `json:"uploaded_at"`
	Content     string `json:"content"` // base64-encoded raw bytes
}

// exportVirtualPrinterHandler produces a complete JSON snapshot of a virtual printer.
// GET /api/printers/:id/export
func (ws *WebServer) exportVirtualPrinterHandler(c *gin.Context) {
	printerID := c.Param("id")

	// Verify it exists and is virtual
	configs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	printer, ok := configs[printerID]
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Printer not found"})
		return
	}
	if !printer.IsVirtual {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Export is only supported for virtual test printers"})
		return
	}

	// Toolhead names
	toolheadNames, err := ws.bridge.GetAllToolheadNames(printerID)
	if err != nil {
		toolheadNames = make(map[int]string)
	}
	// Fill in defaults for any unnamed toolheads
	for i := 0; i < printer.Toolheads; i++ {
		if _, exists := toolheadNames[i]; !exists {
			toolheadNames[i] = fmt.Sprintf("Toolhead %d", i)
		}
	}

	// Spool mappings
	mappings, err := ws.bridge.GetToolheadMappings(printer.Name)
	if err != nil {
		mappings = make(map[int]ToolheadMapping)
	}
	spoolMappings := make(map[int]int)
	for toolheadID, mapping := range mappings {
		if mapping.SpoolID > 0 {
			spoolMappings[toolheadID] = mapping.SpoolID
		}
	}

	// Files (with content)
	filesMeta, err := ws.bridge.GetVirtualPrinterFiles(printerID)
	if err != nil {
		filesMeta = []VirtualPrinterFile{}
	}
	fileExports := make([]VirtualPrinterFileExport, 0, len(filesMeta))
	for _, f := range filesMeta {
		content, _, err := ws.bridge.GetVirtualPrinterFileContent(f.ID)
		if err != nil {
			log.Printf("Warning: could not read content for file %d (%s): %v", f.ID, f.Filename, err)
			continue
		}
		fileExports = append(fileExports, VirtualPrinterFileExport{
			Filename:    f.Filename,
			DisplayName: f.DisplayName,
			FileSize:    f.FileSize,
			UploadedAt:  f.UploadedAt.UTC().Format(time.RFC3339),
			Content:     base64.StdEncoding.EncodeToString(content),
		})
	}

	export := VirtualPrinterExport{
		ExportVersion: 1,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Printer: VirtualPrinterExportMeta{
			Name:      printer.Name,
			Model:     printer.Model,
			Toolheads: printer.Toolheads,
		},
		ToolheadNames: toolheadNames,
		SpoolMappings: spoolMappings,
		Files:         fileExports,
	}

	filename := fmt.Sprintf("virtual-printer-%s.json",
		strings.ReplaceAll(strings.ToLower(printer.Name), " ", "-"))
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.JSON(http.StatusOK, export)
}

// importVirtualPrinterHandler creates a new virtual printer from an export JSON.
// POST /api/printers/import   (multipart field "file" = the .json export)
func (ws *WebServer) importVirtualPrinterHandler(c *gin.Context) {
	ws.operationMutex.Lock()
	defer ws.operationMutex.Unlock()

	if err := c.Request.ParseMultipartForm(200 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse upload"})
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file provided (use field name 'file')"})
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".json") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only .json export files are supported"})
		return
	}

	raw, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file"})
		return
	}

	var export VirtualPrinterExport
	if err := json.Unmarshal(raw, &export); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid export JSON: " + err.Error()})
		return
	}

	if export.ExportVersion != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Unknown export version %d", export.ExportVersion)})
		return
	}
	if export.Printer.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Export is missing printer name"})
		return
	}

	// Create the printer
	printerID := fmt.Sprintf("virtual_%d", time.Now().UnixNano())
	cfg := PrinterConfig{
		Name:      export.Printer.Name,
		Model:     export.Printer.Model,
		IPAddress: "virtual",
		Toolheads: export.Printer.Toolheads,
		IsVirtual: true,
	}
	if cfg.Toolheads < 1 {
		cfg.Toolheads = 1
	}
	if cfg.Model == "" {
		cfg.Model = "Virtual Test Printer"
	}

	if err := ws.bridge.SavePrinterConfig(printerID, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create printer: " + err.Error()})
		return
	}

	// Restore toolhead names
	for toolheadID, name := range export.ToolheadNames {
		defaultName := fmt.Sprintf("Toolhead %d", toolheadID)
		if name != "" && name != defaultName {
			_ = ws.bridge.SetToolheadName(printerID, toolheadID, name)
		}
	}

	// Restore spool mappings
	for toolheadID, spoolID := range export.SpoolMappings {
		if spoolID > 0 {
			if err := ws.bridge.SetToolheadMapping(export.Printer.Name, toolheadID, spoolID); err != nil {
				log.Printf("Warning: could not restore spool mapping toolhead %d → spool %d: %v",
					toolheadID, spoolID, err)
			}
		}
	}

	// Restore G-code files
	filesRestored := 0
	filesSkipped := 0
	for _, f := range export.Files {
		content, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			log.Printf("Warning: could not decode file %s: %v", f.Filename, err)
			filesSkipped++
			continue
		}
		if _, err := ws.bridge.SaveVirtualPrinterFile(printerID, f.Filename, f.DisplayName, content); err != nil {
			log.Printf("Warning: could not restore file %s: %v", f.Filename, err)
			filesSkipped++
			continue
		}
		filesRestored++
	}

	if err := ws.reloadBridgeConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload configuration"})
		return
	}

	log.Printf("✅ Imported virtual printer '%s' (id=%s): %d toolhead(s), %d file(s) restored, %d skipped",
		cfg.Name, printerID, cfg.Toolheads, filesRestored, filesSkipped)

	c.JSON(http.StatusOK, gin.H{
		"message":        "Virtual printer imported successfully",
		"printer_id":     printerID,
		"printer_name":   cfg.Name,
		"toolheads":      cfg.Toolheads,
		"files_restored": filesRestored,
		"files_skipped":  filesSkipped,
		"spool_mappings_note": "Spool IDs from the export have been restored. Verify they exist in your Spoolman instance.",
	})
}

// ─── Spool Assignment Maintenance ────────────────────────────────────────────

// getOrphanedMappingsHandler lists spool assignments whose printer no longer exists.
// GET /api/orphaned-mappings
func (ws *WebServer) getOrphanedMappingsHandler(c *gin.Context) {
	orphans, err := ws.bridge.GetOrphanedMappings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"orphans": orphans, "count": len(orphans)})
}

// clearOrphanedMappingsHandler deletes all orphaned spool assignments.
// DELETE /api/orphaned-mappings
func (ws *WebServer) clearOrphanedMappingsHandler(c *gin.Context) {
	n, err := ws.bridge.ClearOrphanedMappings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Reload config so the dashboard re-fetches available spools
	_ = ws.reloadBridgeConfig()
	if n == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "No orphaned assignments found", "cleared": 0})
	} else {
		c.JSON(http.StatusOK, gin.H{
			"message": fmt.Sprintf("Cleared %d orphaned assignment(s) — spools are now free", n),
			"cleared": n,
		})
	}
}

// ─── OctoPrint Push Handler ──────────────────────────────────────────────────

// receivePrintHandler accepts a completed print record pushed by the OctoPrint plugin.
// POST /api/prints
// Requires X-API-Key header when the_moment_api_key config value is set.
func (ws *WebServer) receivePrintHandler(c *gin.Context) {
	// API key check (optional — skipped when key is unconfigured).
	if storedKey, _ := ws.bridge.GetConfigValue(ConfigKeyTheMomentAPIKey); storedKey != "" {
		if c.GetHeader("X-API-Key") != storedKey {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or missing API key"})
			return
		}
	}

	var payload OctoPrintPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload: " + err.Error()})
		return
	}

	if payload.PrinterID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "printer_id is required"})
		return
	}
	if payload.FileName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_name is required"})
		return
	}
	if payload.EndedAt.IsZero() {
		payload.EndedAt = time.Now()
	}
	if payload.StartedAt.IsZero() {
		payload.StartedAt = payload.EndedAt.Add(-time.Duration(payload.TotalDurationSec) * time.Second)
	}

	if debugMode, _ := ws.bridge.GetConfigValue(ConfigKeyOctoPrintDebug); debugMode == "true" {
		log.Printf("🔍 [OctoPrint debug] print from %s — printer=%q file=%q status=%q tools=%d pauses=%d total_sec=%.0f",
			c.ClientIP(), payload.PrinterID, payload.FileName, payload.Status,
			len(payload.Filament), len(payload.Pauses), payload.TotalDurationSec)
	}

	printID, err := ws.bridge.LogOctoPrintRecord(payload)
	if err != nil {
		log.Printf("Error logging OctoPrint record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save print record"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": printID})
}

// versionHandler returns The Moment's version. No auth required.
// GET /api/version
func (ws *WebServer) versionHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": AppVersion, "name": "The Moment"})
}

// octoprintPingHandler lets the OctoPrint plugin verify connectivity and API-key
// correctness before a real print is sent.
// GET /api/octoprint/ping
func (ws *WebServer) octoprintPingHandler(c *gin.Context) {
	if storedKey, _ := ws.bridge.GetConfigValue(ConfigKeyTheMomentAPIKey); storedKey != "" {
		if c.GetHeader("X-API-Key") != storedKey {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or missing API key"})
			return
		}
	}
	log.Printf("🏓 OctoPrint ping from %s", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"server":    "The Moment",
		"version":   AppVersion,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"message":   "Connection successful. Prints will appear in The Moment's Print History tab.",
	})
}

// ─── Print History Handlers ──────────────────────────────────────────────────

// getSessionsHandler returns print jobs grouped by session_id, newest first.
// GET /api/sessions?limit=200
func (ws *WebServer) getSessionsHandler(c *gin.Context) {
	limit := 200
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	sessions, err := ws.bridge.GetPrintSessions(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions, "count": len(sessions)})
}

// getHistoryHandler returns all print history records (newest first).
// GET /api/history?limit=200
func (ws *WebServer) getHistoryHandler(c *gin.Context) {
	limit := 200
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	records, err := ws.bridge.GetPrintHistory(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"records": records, "count": len(records)})
}

// getHistoryEntryHandler returns a single print history record with full detail.
// GET /api/history/:id
func (ws *WebServer) getHistoryEntryHandler(c *gin.Context) {
	var id int
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	record, err := ws.bridge.GetPrintHistoryEntry(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, record)
}

// updateHistoryNoteHandler sets the user note on a print history record.
// PATCH /api/history/:id/note   body: {"note": "..."}
func (ws *WebServer) updateHistoryNoteHandler(c *gin.Context) {
	var id int
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ws.bridge.UpdatePrintNote(id, body.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Note updated"})
}

// deleteHistoryEntryHandler deletes a print history record.
// DELETE /api/history/:id
func (ws *WebServer) deleteHistoryEntryHandler(c *gin.Context) {
	var id int
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := ws.bridge.DeletePrintHistoryEntry(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Record deleted"})
}

// getHistoryTagsHandler returns quality tags for a print history record.
// GET /api/history/:id/tags
func (ws *WebServer) getHistoryTagsHandler(c *gin.Context) {
	var id int64
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	tags, err := ws.bridge.GetPrintQualityTags(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tags": tags})
}

// setHistoryTagsHandler replaces quality tags for a print history record.
// POST /api/history/:id/tags
func (ws *WebServer) setHistoryTagsHandler(c *gin.Context) {
	var id int64
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var payload PrintTagsPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ws.bridge.SetPrintQualityTags(id, payload); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	tags, _ := ws.bridge.GetPrintQualityTags(id)
	c.JSON(http.StatusOK, gin.H{"tags": tags})
}

// ─── Cost Settings & Calculation Handlers ────────────────────────────────────

// getCostSettingsHandler returns current cost settings.
// GET /api/cost-settings
func (ws *WebServer) getCostSettingsHandler(c *gin.Context) {
	s, err := ws.bridge.GetCostSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s)
}

// setCostSettingsHandler saves cost settings.
// POST /api/cost-settings
func (ws *WebServer) setCostSettingsHandler(c *gin.Context) {
	var s CostSettings
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if s.Currency == "" {
		s.Currency = "USD"
	}
	if err := ws.bridge.SetCostSettings(&s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Cost settings saved"})
}

// getAllPrinterCostSettingsHandler returns per-printer cost overrides for all
// configured printers, merging saved settings with the printer list so the UI
// always has an entry for every printer (zeroes where no override is saved).
// GET /api/cost-settings/printers
func (ws *WebServer) getAllPrinterCostSettingsHandler(c *gin.Context) {
	saved, err := ws.bridge.GetAllPrinterCostSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Index saved settings by printer name for fast lookup.
	byName := make(map[string]*PrinterCostSettings, len(saved))
	for _, s := range saved {
		byName[s.PrinterName] = s
	}

	// Build a result that includes every configured printer, filling in zeros
	// when no override has been saved yet.
	configs, _ := ws.bridge.GetAllPrinterConfigs()
	var result []*PrinterCostSettings
	seen := make(map[string]bool)
	for _, cfg := range configs {
		name := resolvePrinterName(cfg)
		seen[name] = true
		if s, ok := byName[name]; ok {
			result = append(result, s)
		} else {
			result = append(result, &PrinterCostSettings{PrinterName: name})
		}
	}
	// Also include any saved settings for printers not in printer_configs
	// (e.g. OctoPrint printers identified only by printer_id).
	for _, s := range saved {
		if !seen[s.PrinterName] {
			result = append(result, s)
		}
	}
	if result == nil {
		result = []*PrinterCostSettings{}
	}
	c.JSON(http.StatusOK, gin.H{"printers": result})
}

// getPrinterCostSettingsHandler returns per-printer cost overrides.
// GET /api/printers/:id/cost-settings
func (ws *WebServer) getPrinterCostSettingsHandler(c *gin.Context) {
	printerName := ws.resolvePrinterName(c.Param("id"))
	s, err := ws.bridge.GetPrinterCostSettings(printerName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s)
}

// setPrinterCostSettingsHandler saves per-printer cost overrides.
// POST /api/printers/:id/cost-settings
func (ws *WebServer) setPrinterCostSettingsHandler(c *gin.Context) {
	printerName := ws.resolvePrinterName(c.Param("id"))
	var s PrinterCostSettings
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.PrinterName = printerName // authoritative from URL
	if err := ws.bridge.SetPrinterCostSettings(&s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Printer cost settings saved"})
}

// resolvePrinterName maps a printer_id (from printer_configs) to the display
// name used in print_history. Falls back to the raw id if not found.
func (ws *WebServer) resolvePrinterName(printerID string) string {
	configs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		return printerID
	}
	if cfg, ok := configs[printerID]; ok {
		return resolvePrinterName(cfg)
	}
	// Not in printer_configs — treat the id as the printer_name directly
	// (covers OctoPrint printers whose printer_id becomes their printer_name).
	return printerID
}

// calculateCostHandler computes a cost breakdown without persisting it.
// POST /api/cost/calculate
// Body: { filament_grams, print_time_min, spool_id }
func (ws *WebServer) calculateCostHandler(c *gin.Context) {
	var req struct {
		FilamentGrams float64 `json:"filament_grams"`
		PrintTimeMin  float64 `json:"print_time_min"`
		SpoolID       int     `json:"spool_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.FilamentGrams < 0 || req.PrintTimeMin < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "filament_grams and print_time_min must be non-negative"})
		return
	}
	bd, err := ws.bridge.CalculatePrintCost(req.FilamentGrams, req.PrintTimeMin, req.SpoolID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, bd)
}

// getPrintErrorsHandler returns all unacknowledged print errors
func (ws *WebServer) getPrintErrorsHandler(c *gin.Context) {
	errors := ws.bridge.GetPrintErrors()
	c.JSON(http.StatusOK, gin.H{
		"errors": errors,
	})
}

// acknowledgePrintErrorHandler acknowledges a print error
func (ws *WebServer) acknowledgePrintErrorHandler(c *gin.Context) {
	// Ensure we always return JSON
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic in acknowledgePrintErrorHandler: %v", r)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		}
	}()

	errorID := c.Param("id")
	if errorID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error ID is required"})
		return
	}

	if err := ws.bridge.AcknowledgePrintError(errorID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Error acknowledged"})
}

// reloadBridgeConfig reloads the bridge configuration after changes
func (ws *WebServer) reloadBridgeConfig() error {
	// Reload configuration to include changes
	if err := ws.bridge.ReloadConfig(); err != nil {
		return fmt.Errorf("failed to reload configuration: %w", err)
	}
	return nil
}

// Start starts the web server bound to host:port. Pass "0.0.0.0" to listen on all interfaces.
func (ws *WebServer) Start(host, port string) error {
	return ws.router.Run(host + ":" + port)
}

// StartListener starts the web server on an already-bound net.Listener.
// Useful for tests (bind to :0, get assigned port) and socket activation.
func (ws *WebServer) StartListener(l net.Listener) error {
	return ws.router.RunListener(l)
}

// nfcAssignHandler handles NFC tag scans
func (ws *WebServer) nfcAssignHandler(c *gin.Context) {
	spoolIDStr := c.Query("spool")
	locationStr := c.Query("location")
	clientIP := getClientIP(c.ClientIP())

	// Generate session ID based on client IP
	sessionID := generateSessionID(clientIP)

	var spoolID int
	var printerName string
	var toolheadID int
	var err error

	// Parse parameters
	if spoolIDStr != "" {
		spoolID, err = strconv.Atoi(spoolIDStr)
		if err != nil {
			c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{
				"Error": "Invalid spool ID",
			})
			return
		}
	}

	var locationName string
	var isPrinterLocation bool

	if locationStr != "" {
		printerName, toolheadID, locationName, isPrinterLocation, err = ws.bridge.parseLocationParam(locationStr)
		if err != nil {
			c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{
				"Error": err.Error(),
			})
			return
		}
	}

	// Create or update session
	session, err := ws.bridge.createOrUpdateSession(sessionID, spoolID, printerName, toolheadID, locationName, isPrinterLocation)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{
			"Error": "Failed to create session: " + err.Error(),
		})
		return
	}

	// Check if session is complete
	if session.isSessionComplete() {
		// Complete the assignment
		err = ws.bridge.AssignSpoolToLocation(session.SpoolID, session.PrinterName, session.ToolheadID, session.LocationName, session.IsPrinterLocation)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{
				"Error": "Assignment failed: " + err.Error(),
			})
			return
		}

		// Broadcast update to all connected clients
		ws.BroadcastStatus()

		// Clean up session
		ws.bridge.deleteSession(sessionID)

		// Show success page
		c.HTML(http.StatusOK, "nfc_success.html", gin.H{
			"SpoolID":           session.SpoolID,
			"PrinterName":       session.PrinterName,
			"ToolheadID":        session.ToolheadID,
			"IsPrinterLocation": session.IsPrinterLocation,
			"LocationName":      session.LocationName,
		})
		return
	}

	// Session not complete, show progress
	var message string
	if session.HasSpool && !session.HasLocation {
		message = fmt.Sprintf("Spool %d selected. Now scan a location tag.", session.SpoolID)
	} else if session.HasLocation && !session.HasSpool {
		if session.IsPrinterLocation {
			message = fmt.Sprintf("Location %s - Toolhead %d selected. Now scan a spool tag.", session.PrinterName, session.ToolheadID)
		} else {
			message = fmt.Sprintf("Location '%s' selected. Now scan a spool tag.", session.LocationName)
		}
	} else {
		message = "Session started. Scan a spool or location tag."
	}

	c.HTML(http.StatusOK, "nfc_progress.html", gin.H{
		"Message":     message,
		"SessionID":   sessionID,
		"HasSpool":    session.HasSpool,
		"HasLocation": session.HasLocation,
	})
}

// nfcUrlsHandler returns all available NFC URLs with QR codes
func (ws *WebServer) nfcUrlsHandler(c *gin.Context) {
	var urls []gin.H

	// Get all spools
	spools, err := ws.bridge.spoolman.GetAllSpools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Generate spool URLs
	for _, spool := range spools {
		url := fmt.Sprintf("http://%s/api/nfc/assign?spool=%d", c.Request.Host, spool.ID)

		// Safely get color hex
		colorHex := ""
		if spool.Filament != nil && spool.Filament.ColorHex != "" {
			colorHex = spool.Filament.ColorHex
			if !strings.HasPrefix(colorHex, "#") {
				colorHex = "#" + colorHex
			}
		}

		// Extract NFC UUID if one has been assigned.
		nfcID := ""
		tagURL := ""
		if spool.Extra != nil {
			if v, ok := spool.Extra[nfcIDKey]; ok {
				if s, ok := v.(string); ok && s != "" {
					nfcID = s
					tagURL = fmt.Sprintf("http://%s/nfc/spool/%s", c.Request.Host, s)
				}
			}
		}

		// Generate QR code (use UUID tag URL when available, otherwise the legacy assign URL).
		qrTarget := url
		if tagURL != "" {
			qrTarget = tagURL
		}
		qrCode, err := qrcode.Encode(qrTarget, qrcode.Medium, 256)
		if err != nil {
			log.Printf("Error generating QR code for spool %d: %v", spool.ID, err)
			urls = append(urls, gin.H{
				"type":             "spool",
				"spool_id":         spool.ID,
				"spool_name":       spool.Name,
				"material":         spool.Material,
				"brand":            spool.Brand,
				"color_hex":        colorHex,
				"remaining_weight": spool.RemainingWeight,
				"url":              url,
				"nfc_id":           nfcID,
				"tag_url":          tagURL,
				"qr_code_base64":   "",
			})
			continue
		}

		qrCodeBase64 := base64.StdEncoding.EncodeToString(qrCode)
		urls = append(urls, gin.H{
			"type":             "spool",
			"spool_id":         spool.ID,
			"spool_name":       spool.Name,
			"material":         spool.Material,
			"brand":            spool.Brand,
			"color_hex":        colorHex,
			"remaining_weight": spool.RemainingWeight,
			"url":              url,
			"nfc_id":           nfcID,
			"tag_url":          tagURL,
			"qr_code_base64":   qrCodeBase64,
		})
	}

	// Get all filaments
	filaments, err := ws.bridge.spoolman.GetAllFilaments()
	if err != nil {
		log.Printf("Warning: Failed to get filaments for NFC URLs: %v", err)
		filaments = []SpoolmanFilament{}
	}

	// Generate filament URLs
	for _, filament := range filaments {
		url := fmt.Sprintf("%s/filament/show/%d", ws.bridge.config.SpoolmanURL, filament.ID)

		// Safely get color hex
		colorHex := ""
		if filament.ColorHex != "" {
			colorHex = filament.ColorHex
			// Ensure it starts with #
			if !strings.HasPrefix(colorHex, "#") {
				colorHex = "#" + colorHex
			}
		}

		// Get brand name
		brand := "Unknown Brand"
		if filament.Vendor != nil {
			brand = filament.Vendor.Name
		}

		// Generate QR code
		qrCode, err := qrcode.Encode(url, qrcode.Medium, 256)
		if err != nil {
			log.Printf("Error generating QR code for filament %d: %v", filament.ID, err)
			// Continue without QR code if generation fails
			urls = append(urls, gin.H{
				"type":           "filament",
				"filament_id":    filament.ID,
				"filament_name":  filament.Name,
				"material":       filament.Material,
				"brand":          brand,
				"color_hex":      colorHex,
				"extruder_temp":  filament.SettingsExtruderTemp,
				"bed_temp":       filament.SettingsBedTemp,
				"diameter":       filament.Diameter,
				"density":        filament.Density,
				"url":            url,
				"qr_code_base64": "",
			})
			continue
		}

		qrCodeBase64 := base64.StdEncoding.EncodeToString(qrCode)
		urls = append(urls, gin.H{
			"type":           "filament",
			"filament_id":    filament.ID,
			"filament_name":  filament.Name,
			"material":       filament.Material,
			"brand":          brand,
			"color_hex":      colorHex,
			"extruder_temp":  filament.SettingsExtruderTemp,
			"bed_temp":       filament.SettingsBedTemp,
			"diameter":       filament.Diameter,
			"density":        filament.Density,
			"url":            url,
			"qr_code_base64": qrCodeBase64,
		})
	}

	// Get Spoolman locations
	spoolmanLocations, err := ws.bridge.spoolman.GetLocations()
	if err != nil {
		log.Printf("Warning: Failed to get Spoolman locations: %v", err)
		spoolmanLocations = []SpoolmanLocation{}
	}

	// Get printer configurations to build a map of printer toolhead location names
	printerConfigs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		log.Printf("Warning: Failed to get printer configurations: %v", err)
		printerConfigs = make(map[string]PrinterConfig)
	}

	printerLocationNames := make(map[string]bool)
	for printerID, printerConfig := range printerConfigs {
		toolheadNames, err := ws.bridge.GetAllToolheadNames(printerID)
		if err != nil {
			toolheadNames = make(map[int]string)
		}
		for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
			var displayName string
			if name, exists := toolheadNames[toolheadID]; exists {
				displayName = name
			} else {
				displayName = fmt.Sprintf("Toolhead %d", toolheadID)
			}
			locationName := fmt.Sprintf("%s - %s", printerConfig.Name, displayName)
			printerLocationNames[locationName] = true
		}
	}

	// Generate location URLs for Spoolman locations only (no virtual printer toolhead locations)
	for _, location := range spoolmanLocations {
		// Skip archived locations
		if location.Archived {
			continue
		}

		// Skip locations with empty or whitespace-only names
		if strings.TrimSpace(location.Name) == "" {
			continue
		}

		locationParam := location.Name
		nfcUrl := fmt.Sprintf("http://%s/api/nfc/assign?location=%s", c.Request.Host, neturl.QueryEscape(locationParam))

		// Generate QR code
		qrCode, err := qrcode.Encode(nfcUrl, qrcode.Medium, 256)
		if err != nil {
			log.Printf("Error generating QR code for Spoolman location %s: %v", locationParam, err)
			// Continue without QR code if generation fails
			urls = append(urls, gin.H{
				"type":           "location",
				"location_type":  "storage",
				"location_name":  location.Name,
				"display_name":   location.Name,
				"url":            nfcUrl,
				"qr_code_base64": "",
				"is_local_only":  false, // All Spoolman locations are synced
			})
			continue
		}

		qrCodeBase64 := base64.StdEncoding.EncodeToString(qrCode)
		urls = append(urls, gin.H{
			"type":           "location",
			"location_type":  "storage",
			"location_name":  location.Name,
			"display_name":   location.Name,
			"url":            nfcUrl,
			"qr_code_base64": qrCodeBase64,
			"is_local_only":  false, // All Spoolman locations are synced
		})
	}

	// Sort URLs: filaments first, then spools, then locations alphabetically by display name
	sort.Slice(urls, func(i, j int) bool {
		typeI := urls[i]["type"].(string)
		typeJ := urls[j]["type"].(string)

		// Filaments come first, then spools, then locations
		if typeI != typeJ {
			if typeI == "filament" {
				return true
			}
			if typeJ == "filament" {
				return false
			}
			if typeI == "spool" {
				return true
			}
			if typeJ == "spool" {
				return false
			}
			// Both are locations
			return true
		}

		// Both are the same type - apply appropriate sorting
		if typeI == "location" {
			// Locations: sort by display name (case-insensitive)
			displayNameI := urls[i]["display_name"].(string)
			displayNameJ := urls[j]["display_name"].(string)
			return strings.ToLower(displayNameI) < strings.ToLower(displayNameJ)
		}

		if typeI == "filament" {
			// Filaments: sort by ID (same as GetAllFilaments)
			idI := urls[i]["filament_id"].(int)
			idJ := urls[j]["filament_id"].(int)
			return idI < idJ
		}

		if typeI == "spool" {
			// Spools: sort by display name (Material - Brand - Name), then by remaining weight
			// This matches the sorting logic in GetAllSpools()
			materialI := urls[i]["material"].(string)
			materialJ := urls[j]["material"].(string)
			brandI := urls[i]["brand"].(string)
			brandJ := urls[j]["brand"].(string)
			nameI := urls[i]["spool_name"].(string)
			nameJ := urls[j]["spool_name"].(string)

			// Create display names for comparison (same as getSpoolDisplayName())
			displayNameI := fmt.Sprintf("%s - %s - %s", materialI, brandI, nameI)
			displayNameJ := fmt.Sprintf("%s - %s - %s", materialJ, brandJ, nameJ)

			if displayNameI != displayNameJ {
				return displayNameI < displayNameJ
			}

			// If display names are the same, sort by remaining weight (ascending - use less filament first)
			weightI := urls[i]["remaining_weight"].(float64)
			weightJ := urls[j]["remaining_weight"].(float64)
			return weightI < weightJ
		}

		return false
	})

	// Get Spoolman URL for the response
	spoolmanURL := ws.bridge.spoolman.GetBaseURL()

	c.JSON(http.StatusOK, gin.H{
		"urls":         urls,
		"spoolman_url": spoolmanURL,
	})
}

// nfcSessionStatusHandler returns the current session status
func (ws *WebServer) nfcSessionStatusHandler(c *gin.Context) {
	clientIP := getClientIP(c.ClientIP())
	sessionID := generateSessionID(clientIP)

	session, err := ws.bridge.getSession(sessionID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"active": false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"active":              true,
		"session_id":          session.SessionID,
		"has_spool":           session.HasSpool,
		"has_location":        session.HasLocation,
		"spool_id":            session.SpoolID,
		"printer_name":        session.PrinterName,
		"toolhead_id":         session.ToolheadID,
		"location_name":       session.LocationName,
		"is_printer_location": session.IsPrinterLocation,
		"expires_at":          session.ExpiresAt,
	})
}

// Location Management Handlers

// getLocationsHandler returns only Spoolman locations (no virtual printer toolheads)
func (ws *WebServer) getLocationsHandler(c *gin.Context) {
	// Get Spoolman locations
	spoolmanLocations, err := ws.bridge.spoolman.GetLocations()
	if err != nil {
		log.Printf("Warning: Failed to get Spoolman locations: %v", err)
		spoolmanLocations = []SpoolmanLocation{}
	}

	// Only return Spoolman locations (no virtual printer toolhead locations)
	var allLocations []gin.H
	for _, loc := range spoolmanLocations {
		// Skip archived locations
		if loc.Archived {
			continue
		}

		// Skip locations with empty or whitespace-only names
		if strings.TrimSpace(loc.Name) == "" {
			continue
		}

		allLocations = append(allLocations, gin.H{
			"name":       loc.Name,
			"type":       "storage",
			"is_virtual": false,
		})
	}

	// Get Spoolman URL for the message
	spoolmanURL := ws.bridge.spoolman.GetBaseURL()

	c.JSON(http.StatusOK, gin.H{
		"locations":    allLocations,
		"spoolman_url": spoolmanURL,
	})
}

// getLocationStatusHandler returns detailed status information for a specific location
func (ws *WebServer) getLocationStatusHandler(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Location name is required"})
		return
	}

	// Check if location exists in Spoolman
	location, err := ws.bridge.spoolman.FindLocationByName(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if location == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Location not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name":     location.Name,
		"id":       location.ID,
		"comment":  location.Comment,
		"archived": location.Archived,
	})
}

// createLocationHandler creates a new location in Spoolman
func (ws *WebServer) createLocationHandler(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Type        string `json:"type"`
		PrinterName string `json:"printer_name,omitempty"`
		ToolheadID  int    `json:"toolhead_id,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("createLocationHandler: bad request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("createLocationHandler: creating location name='%s' in Spoolman", req.Name)
	location, err := ws.bridge.spoolman.GetOrCreateLocation(req.Name)
	if err != nil {
		log.Printf("createLocationHandler: failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"name":     location.Name,
		"id":       location.ID,
		"comment":  location.Comment,
		"archived": location.Archived,
	})
}

// updateLocationHandler updates a location in Spoolman
func (ws *WebServer) updateLocationHandler(c *gin.Context) {
	oldName := c.Param("name")
	if oldName == "" {
		log.Printf("updateLocationHandler: missing location name")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Location name is required"})
		return
	}

	var req struct {
		Name string `json:"name" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("updateLocationHandler: bad request for name='%s': %v", oldName, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("updateLocationHandler: renaming '%s' to '%s' in Spoolman", oldName, req.Name)
	if err := ws.bridge.spoolman.UpdateLocationByName(oldName, req.Name); err != nil {
		log.Printf("updateLocationHandler: failed for name='%s': %v", oldName, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get updated location
	location, err := ws.bridge.spoolman.FindLocationByName(req.Name)
	if err != nil {
		log.Printf("Warning: Could not get updated location '%s': %v", req.Name, err)
		c.JSON(http.StatusOK, gin.H{"message": "Location updated successfully"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Location updated successfully",
		"location": gin.H{
			"name":     location.Name,
			"id":       location.ID,
			"comment":  location.Comment,
			"archived": location.Archived,
		},
	})
}

// deleteLocationHandler archives a location in Spoolman (locations are archived, not deleted)
func (ws *WebServer) deleteLocationHandler(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		log.Printf("deleteLocationHandler: missing location name")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Location name is required"})
		return
	}

	// Find location by name
	location, err := ws.bridge.spoolman.FindLocationByName(name)
	if err != nil {
		log.Printf("deleteLocationHandler: error finding location '%s': %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if location == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Location not found"})
		return
	}

	// Archive the location (Spoolman doesn't support deletion, only archiving)
	log.Printf("deleteLocationHandler: archiving location '%s' (ID: %d)", name, location.ID)
	if err := ws.bridge.spoolman.ArchiveLocation(location.ID); err != nil {
		log.Printf("deleteLocationHandler: failed to archive location '%s': %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to archive location"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Location archived successfully"})
}

// ─── NFC spool-UUID (OpenPrintTag) browser handlers ──────────────────────────

// nfcSpoolScanHandler renders the mobile scan page for a UUID-tagged spool.
// GET /nfc/spool/:uuid
func (ws *WebServer) nfcSpoolScanHandler(c *gin.Context) {
	nfcUUID := c.Param("uuid")

	spool, err := ws.bridge.spoolman.GetSpoolByNFCTag(nfcUUID)
	if err != nil || spool == nil {
		c.HTML(http.StatusNotFound, "nfc_error.html", gin.H{
			"Error": "No spool is associated with this NFC tag. Use The Moment's NFC tab to assign a spool.",
		})
		return
	}

	// Build printer + toolhead list with current spool mappings.
	printerConfigs, _ := ws.bridge.GetAllPrinterConfigs()
	allMappings, _ := ws.bridge.GetAllToolheadMappings()

	type toolheadEntry struct {
		ID           int
		DisplayName  string
		CurrentSpool int
	}
	type printerEntry struct {
		Name      string
		Toolheads []toolheadEntry
	}
	var printers []printerEntry
	for _, pc := range printerConfigs {
		if pc.IsVirtual {
			continue
		}
		var ths []toolheadEntry
		for i := 0; i < pc.Toolheads; i++ {
			curSpool := 0
			if pm, ok := allMappings[pc.Name]; ok {
				if m, ok := pm[i]; ok {
					curSpool = m.SpoolID
				}
			}
			ths = append(ths, toolheadEntry{ID: i, DisplayName: fmt.Sprintf("Toolhead %d", i), CurrentSpool: curSpool})
		}
		printers = append(printers, printerEntry{Name: pc.Name, Toolheads: ths})
	}

	locations, _ := ws.bridge.spoolman.GetLocations()
	var locNames []string
	for _, l := range locations {
		if !l.Archived && strings.TrimSpace(l.Name) != "" {
			locNames = append(locNames, l.Name)
		}
	}

	trashLoc, _ := ws.bridge.GetConfigValue(ConfigKeyNFCTrashLocation)
	invLoc, _ := ws.bridge.GetConfigValue(ConfigKeyNFCInventoryLocation)

	colorHex := ""
	if spool.Filament != nil && spool.Filament.ColorHex != "" {
		colorHex = spool.Filament.ColorHex
		if !strings.HasPrefix(colorHex, "#") {
			colorHex = "#" + colorHex
		}
	}
	if colorHex == "" {
		colorHex = "#888888"
	}

	c.HTML(http.StatusOK, "nfc_spool_uuid.html", gin.H{
		"NfcUUID":           nfcUUID,
		"SpoolID":           spool.ID,
		"SpoolName":         spool.Name,
		"Material":          spool.Material,
		"Brand":             spool.Brand,
		"ColorHex":          colorHex,
		"RemainingWeight":   fmt.Sprintf("%.0f", spool.RemainingWeight),
		"InitialWeight":     fmt.Sprintf("%.0f", spool.InitialWeight),
		"CurrentLocation":   spool.Location,
		"Printers":          printers,
		"Locations":         locNames,
		"TrashLocation":     trashLoc,
		"InventoryLocation": invLoc,
	})
}

// nfcSpoolAssignHandler processes the assignment form from the scan page.
// POST /nfc/spool/:uuid/assign
func (ws *WebServer) nfcSpoolAssignHandler(c *gin.Context) {
	nfcUUID := c.Param("uuid")

	spool, err := ws.bridge.spoolman.GetSpoolByNFCTag(nfcUUID)
	if err != nil || spool == nil {
		c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{"Error": "Spool not found for this tag."})
		return
	}

	targetType := c.PostForm("target_type") // "toolhead" | "location" | "trash"
	trashLoc, _ := ws.bridge.GetConfigValue(ConfigKeyNFCTrashLocation)
	invLoc, _ := ws.bridge.GetConfigValue(ConfigKeyNFCInventoryLocation)

	switch targetType {
	case "toolhead":
		printerName := c.PostForm("printer_name")
		toolheadIDStr := c.PostForm("toolhead_id")
		toolheadID, _ := strconv.Atoi(toolheadIDStr)

		// Check if toolhead already has a spool.
		existingSpoolID, _ := ws.bridge.GetToolheadMapping(printerName, toolheadID)
		if existingSpoolID > 0 && existingSpoolID != spool.ID {
			// Displacement needed — redirect to the displaced spool page.
			existingSpool, _ := ws.bridge.spoolman.GetSpoolByID(existingSpoolID)
			oldName := fmt.Sprintf("Spool #%d", existingSpoolID)
			if existingSpool != nil && existingSpool.Name != "" {
				oldName = existingSpool.Name
			}
			redirectURL := fmt.Sprintf("/nfc/spool/%s/displaced?old_id=%d&old_name=%s&printer=%s&toolhead=%d&new_spool=%d&inventory=%s&trash=%s",
				nfcUUID,
				existingSpoolID,
				neturl.QueryEscape(oldName),
				neturl.QueryEscape(printerName),
				toolheadID,
				spool.ID,
				neturl.QueryEscape(invLoc),
				neturl.QueryEscape(trashLoc),
			)
			c.Redirect(http.StatusSeeOther, redirectURL)
			return
		}

		// No displacement — assign directly.
		locationName := fmt.Sprintf("%s - Toolhead %d", printerName, toolheadID)
		if err := ws.bridge.AssignSpoolToLocation(spool.ID, printerName, toolheadID, locationName, true); err != nil {
			c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{"Error": "Assignment failed: " + err.Error()})
			return
		}
		ws.BroadcastStatus()
		c.HTML(http.StatusOK, "nfc_success.html", gin.H{
			"SpoolID": spool.ID, "PrinterName": printerName, "ToolheadID": toolheadID,
			"IsPrinterLocation": true, "LocationName": locationName,
		})

	case "location":
		locationName := c.PostForm("location_name")
		if locationName == "" {
			c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{"Error": "Location name is required."})
			return
		}
		if err := ws.bridge.AssignSpoolToLocation(spool.ID, "", 0, locationName, false); err != nil {
			c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{"Error": "Assignment failed: " + err.Error()})
			return
		}
		ws.BroadcastStatus()
		c.HTML(http.StatusOK, "nfc_success.html", gin.H{
			"SpoolID": spool.ID, "IsPrinterLocation": false, "LocationName": locationName,
		})

	case "trash":
		if trashLoc == "" {
			c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{"Error": "No Trash location configured. Set it in The Moment Settings → Advanced → NFC Locations."})
			return
		}
		if err := ws.bridge.AssignSpoolToLocation(spool.ID, "", 0, trashLoc, false); err != nil {
			c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{"Error": "Failed to move spool to trash: " + err.Error()})
			return
		}
		ws.BroadcastStatus()
		c.HTML(http.StatusOK, "nfc_success.html", gin.H{
			"SpoolID": spool.ID, "IsPrinterLocation": false, "LocationName": trashLoc,
		})

	default:
		c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{"Error": "Invalid target type."})
	}
}

// nfcSpoolDisplacedHandler shows the displaced-spool destination picker.
// GET /nfc/spool/:uuid/displaced?old_id=X&old_name=Y&printer=Z&toolhead=N&new_spool=M&inventory=...&trash=...
func (ws *WebServer) nfcSpoolDisplacedHandler(c *gin.Context) {
	nfcUUID := c.Param("uuid")
	oldIDStr := c.Query("old_id")
	oldName := c.Query("old_name")
	printerName := c.Query("printer")
	toolheadIDStr := c.Query("toolhead")
	newSpoolIDStr := c.Query("new_spool")
	invLoc := c.Query("inventory")
	trashLoc := c.Query("trash")

	locations, _ := ws.bridge.spoolman.GetLocations()
	var locNames []string
	for _, l := range locations {
		if !l.Archived && strings.TrimSpace(l.Name) != "" {
			locNames = append(locNames, l.Name)
		}
	}

	c.HTML(http.StatusOK, "nfc_displaced.html", gin.H{
		"NfcUUID":           nfcUUID,
		"OldSpoolID":        oldIDStr,
		"OldSpoolName":      oldName,
		"PrinterName":       printerName,
		"ToolheadID":        toolheadIDStr,
		"NewSpoolID":        newSpoolIDStr,
		"InventoryLocation": invLoc,
		"TrashLocation":     trashLoc,
		"Locations":         locNames,
	})
}

// nfcSpoolCompleteHandler finalises the two-step NFC assignment (new spool + displaced spool).
// POST /nfc/spool/:uuid/complete
func (ws *WebServer) nfcSpoolCompleteHandler(c *gin.Context) {
	nfcUUID := c.Param("uuid")

	newSpoolID, _ := strconv.Atoi(c.PostForm("new_spool_id"))
	printerName := c.PostForm("printer_name")
	toolheadID, _ := strconv.Atoi(c.PostForm("toolhead_id"))
	displacedSpoolID, _ := strconv.Atoi(c.PostForm("displaced_spool_id"))
	displacedLocation := c.PostForm("displaced_location")

	if newSpoolID == 0 {
		c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{"Error": "Missing spool information."})
		return
	}

	// Move displaced spool to chosen location first.
	if displacedSpoolID > 0 && displacedLocation != "" {
		if err := ws.bridge.AssignSpoolToLocation(displacedSpoolID, "", 0, displacedLocation, false); err != nil {
			log.Printf("nfcSpoolCompleteHandler: failed to relocate displaced spool %d: %v", displacedSpoolID, err)
		}
	}

	// Assign the new spool to the toolhead.
	locationName := fmt.Sprintf("%s - Toolhead %d", printerName, toolheadID)
	if err := ws.bridge.AssignSpoolToLocation(newSpoolID, printerName, toolheadID, locationName, true); err != nil {
		c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{"Error": "Final assignment failed: " + err.Error()})
		return
	}

	ws.BroadcastStatus()
	log.Printf("✅ NFC UUID %s: spool %d → %s toolhead %d; displaced spool %d → %s",
		nfcUUID, newSpoolID, printerName, toolheadID, displacedSpoolID, displacedLocation)

	c.HTML(http.StatusOK, "nfc_success.html", gin.H{
		"SpoolID":           newSpoolID,
		"PrinterName":       printerName,
		"ToolheadID":        toolheadID,
		"IsPrinterLocation": true,
		"LocationName":      locationName,
	})
}

// ─── NFC tag management API ───────────────────────────────────────────────────

// nfcAssignTagHandler writes or generates a UUID for a spool and returns the tag URL + QR code.
// POST /api/nfc/spool/:id/tag
// Body (optional): {"nfc_id": "your-uuid"}  — omit to auto-generate.
func (ws *WebServer) nfcAssignTagHandler(c *gin.Context) {
	spoolID, err := strconv.Atoi(c.Param("id"))
	if err != nil || spoolID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid spool ID"})
		return
	}

	var body struct {
		NfcID string `json:"nfc_id"`
	}
	_ = c.ShouldBindJSON(&body) // optional body

	nfcUUID := body.NfcID
	if nfcUUID == "" {
		nfcUUID = newSessionID() // reuse existing UUID v4 generator
	}

	if err := ws.bridge.spoolman.SetSpoolNFCTag(spoolID, nfcUUID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	tagURL := fmt.Sprintf("http://%s/nfc/spool/%s", c.Request.Host, nfcUUID)
	qrBytes, _ := qrcode.Encode(tagURL, qrcode.Medium, 256)
	qrB64 := base64.StdEncoding.EncodeToString(qrBytes)

	log.Printf("🏷️  NFC tag assigned to spool %d: uuid=%s url=%s", spoolID, nfcUUID, tagURL)
	c.JSON(http.StatusOK, gin.H{
		"spool_id":       spoolID,
		"nfc_id":         nfcUUID,
		"tag_url":        tagURL,
		"qr_code_base64": qrB64,
	})
}

// nfcRemoveTagHandler clears the NFC UUID from a spool's extra fields.
// DELETE /api/nfc/spool/:id/tag
func (ws *WebServer) nfcRemoveTagHandler(c *gin.Context) {
	spoolID, err := strconv.Atoi(c.Param("id"))
	if err != nil || spoolID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid spool ID"})
		return
	}
	if err := ws.bridge.spoolman.ClearSpoolNFCTag(spoolID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("🏷️  NFC tag removed from spool %d", spoolID)
	c.JSON(http.StatusOK, gin.H{"message": "NFC tag removed"})
}

// nfcConfigHandler returns the configured NFC trash and inventory location names.
// GET /api/nfc/config
func (ws *WebServer) nfcConfigHandler(c *gin.Context) {
	trash, _ := ws.bridge.GetConfigValue(ConfigKeyNFCTrashLocation)
	inv, _ := ws.bridge.GetConfigValue(ConfigKeyNFCInventoryLocation)
	c.JSON(http.StatusOK, gin.H{
		"trash_location":     trash,
		"inventory_location": inv,
	})
}

// nfcSaveConfigHandler saves the NFC trash and inventory location names.
// POST /api/nfc/config
func (ws *WebServer) nfcSaveConfigHandler(c *gin.Context) {
	var body struct {
		TrashLocation     string `json:"trash_location"`
		InventoryLocation string `json:"inventory_location"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ws.bridge.SetConfigValue(ConfigKeyNFCTrashLocation, body.TrashLocation); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := ws.bridge.SetConfigValue(ConfigKeyNFCInventoryLocation, body.InventoryLocation); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "NFC config saved"})
}

// ─── Post-print filament segment reassignment ─────────────────────────────────

// reassignFilamentHandler moves a print's filament segment to a different spool.
// POST /api/prints/:id/filament/:segment_id/reassign
// Body: {"spool_id": 42}
func (ws *WebServer) reassignFilamentHandler(c *gin.Context) {
	printID, err := strconv.Atoi(c.Param("id"))
	if err != nil || printID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid print ID"})
		return
	}
	segmentID, err := strconv.Atoi(c.Param("segment_id"))
	if err != nil || segmentID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid segment ID"})
		return
	}

	var body struct {
		SpoolID int `json:"spool_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := ws.bridge.ReassignFilamentSegment(printID, segmentID, body.SpoolID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "segment reassigned", "new_spool_id": body.SpoolID})
}
