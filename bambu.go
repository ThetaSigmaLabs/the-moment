// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

// =============================================================================
// bambu.go
// =============================================================================
// Bambu printer MQTT client and types.
//
// Bambu printers communicate via MQTT over TLS on port 8883 to a broker
// running on the printer itself. This file provides:
//
//   - BambuStatusProvider interface — abstracts the MQTT transport so tests
//     can inject a mock without needing a real printer or broker.
//   - BambuMQTTClient — the real implementation using paho.mqtt.golang.
//   - BambuStatus / AMSSlotInfo — the normalised status types used by the
//     bridge state machine.
//   - Debug logging helpers — enable with BAMBU_DEBUG=1 env var or the
//     "bambu_debug" DB config key. Logs every raw MQTT payload, TLS handshake
//     outcome, and parsed status so a first-time user can capture a transcript
//     and share it for integration troubleshooting.
//
// Credential encoding
//
// The PrinterConfig struct stores Bambu credentials in the existing fields:
//   IPAddress = the printer's LAN IP (e.g. "192.168.1.50")
//   APIKey    = "serial:accesscode" (e.g. "00M09C380500000:abc123ef")
//
// The serial number is required to build the MQTT topic
// ("device/{serial}/report"). The access code is the MQTT password.
// Username is always "bblp".
// =============================================================================

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// ─── Types ────────────────────────────────────────────────────────────────────

// AMSSlotInfo holds the current state of a single AMS tray.
type AMSSlotInfo struct {
	ID     int    // flat index: (amsUnitIndex * 4) + trayIndex
	Remain int    // 0-100 percent remaining
	Type   string // filament type string e.g. "PLA", "ABS", "PETG"
	Color  string // RRGGBBAA hex e.g. "FFFFFFFF"
}

// BambuStatus is the normalised printer state derived from MQTT report payloads.
// Bambu sends incremental updates — only changed fields are present in each
// message. BambuMQTTClient merges incoming payloads so GetCurrentStatus always
// returns the most up-to-date composite picture.
type BambuStatus struct {
	GcodeState          string              // "IDLE","RUNNING","PAUSE","FINISH","FAILED","CANCEL",""
	McPercent           int                 // 0–100 print progress
	GcodeFile           string              // filename of the active/last job
	FilamentWeightTotal float64             // grams consumed reported at FINISH
	AMSSlots            map[int]AMSSlotInfo // flat slot index → tray state
}

// BambuStatusProvider abstracts the MQTT transport so the bridge state machine
// and tests can operate without a real Bambu printer.
type BambuStatusProvider interface {
	// Connect establishes the MQTT subscription. Idempotent — safe to call
	// multiple times; subsequent calls are no-ops if already connected.
	Connect() error
	// GetCurrentStatus returns the last merged status received from the printer.
	// Returns an error if no status message has been received yet (still connecting).
	GetCurrentStatus() (*BambuStatus, error)
	// Close disconnects the MQTT client cleanly.
	Close() error
}

// ─── Real MQTT client ─────────────────────────────────────────────────────────

// BambuMQTTClient implements BambuStatusProvider using Eclipse Paho MQTT.
type BambuMQTTClient struct {
	ip         string
	serial     string
	accessCode string

	mu          sync.RWMutex
	client      mqtt.Client
	lastStatus  *BambuStatus
	connected   bool
	debugLogger *log.Logger // non-nil when debug mode is active
}

// NewBambuMQTTClient constructs a client. Call Connect() to establish the
// MQTT session and begin receiving status updates.
func NewBambuMQTTClient(ip, serial, accessCode string, debugLogger *log.Logger) *BambuMQTTClient {
	return &BambuMQTTClient{
		ip:          ip,
		serial:      serial,
		accessCode:  accessCode,
		debugLogger: debugLogger,
	}
}

// Connect establishes a TLS MQTT connection to the printer and subscribes to
// the report topic. Bambu uses a self-signed certificate, so TLS verification
// is intentionally skipped.
func (c *BambuMQTTClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil
	}

	broker := fmt.Sprintf("tls://%s:8883", c.ip)
	topic := fmt.Sprintf("device/%s/report", c.serial)

	c.debugLog("Connecting to %s serial=%s", broker, c.serial)

	tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // Bambu uses self-signed cert

	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetUsername("bblp").
		SetPassword(c.accessCode).
		SetTLSConfig(tlsCfg).
		SetAutoReconnect(true).
		SetConnectRetryInterval(30 * time.Second).
		SetConnectTimeout(10 * time.Second).
		SetClientID(fmt.Sprintf("the-moment-%s", c.serial)).
		SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
			c.handleReport(msg.Payload())
		}).
		SetOnConnectHandler(func(_ mqtt.Client) {
			c.debugLog("MQTT connected, subscribing to %s", topic)
			if token := c.client.Subscribe(topic, 0, nil); token.Wait() && token.Error() != nil {
				log.Printf("[BAMBU] Warning: subscribe failed for %s: %v", topic, token.Error())
			} else {
				c.debugLog("Subscribed to topic: %s", topic)
			}
			c.mu.Lock()
			c.connected = true
			c.mu.Unlock()
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			log.Printf("[BAMBU] Connection lost to %s: %v (auto-reconnect enabled)", c.ip, err)
			c.mu.Lock()
			c.connected = false
			c.mu.Unlock()
		})

	// When debug mode is on, wire Paho's internal loggers so TLS negotiation
	// details, CONNACK codes, and reconnect events appear in the log.
	if c.debugLogger != nil {
		mqtt.DEBUG = log.New(os.Stdout, "[BAMBU MQTT] ", 0)
		mqtt.WARN = log.New(os.Stdout, "[BAMBU MQTT WARN] ", 0)
		mqtt.ERROR = log.New(os.Stderr, "[BAMBU MQTT ERROR] ", 0)
	}

	client := mqtt.NewClient(opts)
	c.client = client

	token := client.Connect()
	if !token.WaitTimeout(15 * time.Second) {
		return fmt.Errorf("MQTT connect timeout to %s", c.ip)
	}
	if err := token.Error(); err != nil {
		c.debugLog("TLS/MQTT connect error: %v", err)
		return fmt.Errorf("MQTT connect to %s: %w", c.ip, err)
	}

	c.debugLog("TLS handshake and MQTT connect succeeded")
	return nil
}

// GetCurrentStatus returns a copy of the last merged status.
// Returns an error if no status has been received yet.
func (c *BambuMQTTClient) GetCurrentStatus() (*BambuStatus, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastStatus == nil {
		return nil, fmt.Errorf("no status received yet from Bambu printer %s", c.ip)
	}
	// Return a shallow copy with a new AMSSlots map to prevent data races
	s := *c.lastStatus
	slots := make(map[int]AMSSlotInfo, len(c.lastStatus.AMSSlots))
	for k, v := range c.lastStatus.AMSSlots {
		slots[k] = v
	}
	s.AMSSlots = slots
	return &s, nil
}

// Close disconnects the MQTT client gracefully.
func (c *BambuMQTTClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.client.IsConnected() {
		c.client.Disconnect(500)
	}
	c.connected = false
	return nil
}

// ─── Internal MQTT handling ───────────────────────────────────────────────────

// bambuReport is the top-level MQTT report payload shape.
type bambuReport struct {
	Print bambuPrintReport `json:"print"`
}

type bambuPrintReport struct {
	GcodeState          string          `json:"gcode_state"`
	McPercent           int             `json:"mc_percent"`
	GcodeFile           string          `json:"gcode_file"`
	McRemainingTime     int             `json:"mc_remaining_time"`
	FilamentWeightTotal float64         `json:"filament_weight_total"`
	AMS                 *bambuAMSReport `json:"ams,omitempty"`
}

type bambuAMSReport struct {
	AMS []bambuAMSUnit `json:"ams"`
}

type bambuAMSUnit struct {
	ID   string         `json:"id"`
	Tray []bambuAMSTray `json:"tray"`
}

type bambuAMSTray struct {
	ID     string `json:"id"`
	Remain int    `json:"remain"`
	Type   string `json:"tray_type"`
	Color  string `json:"tray_color"`
}

// handleReport parses an incoming MQTT payload and merges it into lastStatus.
// Bambu sends incremental updates — only fields that changed are present.
// We merge rather than replace so partial messages don't erase known state.
func (c *BambuMQTTClient) handleReport(payload []byte) {
	c.debugLog("Raw MQTT payload (%d bytes): %s", len(payload), string(payload))

	var report bambuReport
	if err := json.Unmarshal(payload, &report); err != nil {
		log.Printf("[BAMBU] Warning: failed to parse MQTT payload from %s: %v", c.ip, err)
		return
	}

	p := report.Print

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastStatus == nil {
		c.lastStatus = &BambuStatus{
			AMSSlots: make(map[int]AMSSlotInfo),
		}
	}

	// Merge non-zero/non-empty fields only (Bambu incremental update semantics)
	if p.GcodeState != "" {
		c.debugLog("Incremental merge: gcode_state updated %q → %q", c.lastStatus.GcodeState, p.GcodeState)
		c.lastStatus.GcodeState = p.GcodeState
	}
	if p.McPercent != 0 {
		c.lastStatus.McPercent = p.McPercent
	}
	if p.GcodeFile != "" {
		c.lastStatus.GcodeFile = p.GcodeFile
	}
	if p.FilamentWeightTotal != 0 {
		c.lastStatus.FilamentWeightTotal = p.FilamentWeightTotal
	}

	// Merge AMS tray data
	if p.AMS != nil {
		for unitIdx, unit := range p.AMS.AMS {
			for _, tray := range unit.Tray {
				var trayIdx int
				fmt.Sscanf(tray.ID, "%d", &trayIdx)
				slotIdx := (unitIdx * 4) + trayIdx
				c.debugLog("AMS unit %s tray %s: type=%s color=%s remain=%d%%",
					unit.ID, tray.ID, tray.Type, tray.Color, tray.Remain)
				c.lastStatus.AMSSlots[slotIdx] = AMSSlotInfo{
					ID:     slotIdx,
					Remain: tray.Remain,
					Type:   tray.Type,
					Color:  tray.Color,
				}
			}
		}
	}

	c.debugLog("Parsed status: gcode_state=%q mc_percent=%d gcode_file=%q filament_weight_total=%.2fg ams_slots=%d",
		c.lastStatus.GcodeState, c.lastStatus.McPercent, c.lastStatus.GcodeFile,
		c.lastStatus.FilamentWeightTotal, len(c.lastStatus.AMSSlots))
}

// debugLog emits a log line when debug mode is active.
func (c *BambuMQTTClient) debugLog(format string, args ...interface{}) {
	if c.debugLogger != nil {
		c.debugLogger.Printf(format, args...)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// parseBambuCredentials splits an APIKey of the form "serial:accesscode" into
// its components. Returns an error if the format is invalid.
func parseBambuCredentials(apiKey string) (serial, accessCode string, err error) {
	parts := strings.SplitN(apiKey, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("bambu api_key must be 'serial:accesscode', got %q", apiKey)
	}
	return parts[0], parts[1], nil
}

// mapBambuState converts a Bambu gcode_state string to a The Moment state constant.
func mapBambuState(gcodeState string) string {
	switch gcodeState {
	case "RUNNING":
		return StatePrinting
	case "PAUSE":
		return StatePaused
	case "FINISH":
		return StateFinished
	case "IDLE":
		return StateIdle
	case "FAILED", "CANCEL":
		return StateStopped
	case "":
		return StateOffline // not yet received a status message
	default:
		return gcodeState
	}
}

// newBambuDebugLogger returns a debug logger if Bambu debug mode is enabled,
// either via the BAMBU_DEBUG env var or the bridge DB config value.
// Returns nil when debug mode is off.
func newBambuDebugLogger(b *FilamentBridge) *log.Logger {
	if os.Getenv("BAMBU_DEBUG") == "1" {
		return log.New(os.Stdout, "[BAMBU DEBUG] ", log.LstdFlags)
	}
	if b != nil {
		if val, err := b.GetConfigValue(ConfigKeyBambuDebug); err == nil && val == "true" {
			return log.New(os.Stdout, "[BAMBU DEBUG] ", log.LstdFlags)
		}
	}
	return nil
}
