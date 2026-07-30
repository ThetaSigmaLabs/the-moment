// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker.go
// =============================================================================
// WebSocket client for Moonraker (Klipper) printers.
//
// Shaped after the Bambu client rather than the PrusaLink one: Bambu is the
// existing printer type that holds a persistent connection and caches state
// for the dashboard to read, which is exactly what is needed here. PrusaLink's
// request/response pattern does not fit.
//
// Why a WebSocket and not a poller. The extrusion tracker needs every extruder
// position to compute a correct high-water mark. A 30s poll would miss tool
// changes and could miss the peak either side of a Klipper restart, so the
// socket is load-bearing for billing accuracy, not a nicety for live status.
//
// Subscribed objects, and why each one:
//
//	toolhead.position[3]  the raw kinematic extruder axis — the tracker's input.
//	                      NEVER gcode_move.gcode_position: G92 rewrites that
//	                      one's offset, and feeding it silently loses extrusion
//	                      on every slicer E-reset.
//	toolhead.extruder     active extruder name; a change rebaselines the tracker.
//	print_stats           state/filename/filament_used/duration, plus info's
//	                      current_layer and total_layer, which PrusaLink's API
//	                      cannot provide at all.
//	virtual_sdcard        progress.
//	extruder / heater_bed temperatures for the dashboard tile.
//
// This file owns transport and state. Billing arithmetic lives in
// ExtrusionTracker (moonraker_tracker.go) and is deliberately kept free of I/O.
// =============================================================================

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Moonraker's default HTTP/WebSocket port.
const MoonrakerDefaultPort = 7125

// Reconnect backoff bounds. Moonraker restarts (and Klipper firmware restarts)
// are routine, so reconnecting must be cheap and quiet.
const (
	moonrakerMinBackoff = 2 * time.Second
	moonrakerMaxBackoff = 30 * time.Second
	moonrakerWriteWait  = 10 * time.Second
	moonrakerPongWait   = 90 * time.Second
	moonrakerPingPeriod = 30 * time.Second
)

// MoonrakerStatus is the cached snapshot the dashboard reads. It mirrors the
// role of the Bambu client's status: the poll loop and the web layer read this
// rather than opening a connection of their own.
type MoonrakerStatus struct {
	Connected   bool
	KlippyReady bool

	State    string // print_stats.state: standby|printing|paused|complete|cancelled|error
	Filename string

	Progress      float64 // 0..1, from virtual_sdcard
	FilamentUsed  float64 // mm, as reported by Klipper for this job
	PrintDuration float64 // seconds
	TotalDuration float64 // seconds

	CurrentLayer int
	TotalLayer   int

	NozzleTemp   float64
	NozzleTarget float64
	BedTemp      float64
	BedTarget    float64

	LastUpdate time.Time
}

// MoonrakerStatusProvider is the seam the dashboard and tests depend on,
// mirroring BambuStatusProvider so a fake can stand in for a live printer.
type MoonrakerStatusProvider interface {
	GetCurrentStatus() (MoonrakerStatus, error)
	Tracker() *ExtrusionTracker
	Close()
}

// MoonrakerClient maintains one WebSocket connection to one printer.
type MoonrakerClient struct {
	host string // "10.49.9.130" or "10.49.9.130:7125"

	tracker *ExtrusionTracker

	mu     sync.RWMutex
	status MoonrakerStatus

	conn   *websocket.Conn
	connMu sync.Mutex

	nextID int
	idMu   sync.Mutex

	done     chan struct{}
	closeOne sync.Once

	// debugLog mirrors PrinterConfig.DebugLog: extrusion updates arrive many
	// times a second, so per-update logging must be opt-in.
	debugLog bool
}

var _ MoonrakerStatusProvider = (*MoonrakerClient)(nil)

// NewMoonrakerClient creates a client and starts its connection loop. The
// caller owns the returned client and must Close it.
func NewMoonrakerClient(host string, debugLog bool) *MoonrakerClient {
	c := &MoonrakerClient{
		host:     normalizeMoonrakerHost(host),
		tracker:  NewExtrusionTracker(),
		done:     make(chan struct{}),
		debugLog: debugLog,
	}
	go c.run()
	return c
}

// normalizeMoonrakerHost appends Moonraker's default port when the configured
// address omits one. Printer config stores a bare IP for PrusaLink, so the same
// field will arrive here without a port.
func normalizeMoonrakerHost(host string) string {
	for i := len(host) - 1; i >= 0; i-- {
		switch host[i] {
		case ':':
			return host
		case '.', ']':
			// IPv4 or bracketed IPv6 with no port.
			return fmt.Sprintf("%s:%d", host, MoonrakerDefaultPort)
		}
	}
	return fmt.Sprintf("%s:%d", host, MoonrakerDefaultPort)
}

// Tracker exposes the extrusion tracker so the flush loop can drain it.
func (c *MoonrakerClient) Tracker() *ExtrusionTracker { return c.tracker }

// GetCurrentStatus returns the cached snapshot. It never blocks on the network,
// so the dashboard stays responsive while a printer is unreachable.
func (c *MoonrakerClient) GetCurrentStatus() (MoonrakerStatus, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.status.Connected {
		return c.status, fmt.Errorf("moonraker %s: not connected", c.host)
	}
	return c.status, nil
}

// Close tears down the connection loop. Safe to call more than once.
func (c *MoonrakerClient) Close() {
	c.closeOne.Do(func() {
		close(c.done)
		c.connMu.Lock()
		if c.conn != nil {
			_ = c.conn.Close()
		}
		c.connMu.Unlock()
	})
}

// run dials, serves, and redials with capped exponential backoff until Close.
func (c *MoonrakerClient) run() {
	backoff := moonrakerMinBackoff
	for {
		select {
		case <-c.done:
			return
		default:
		}

		if err := c.connectAndServe(); err != nil {
			if c.debugLog {
				log.Printf("Moonraker %s: connection ended: %v", c.host, err)
			}
		}

		c.markDisconnected()

		select {
		case <-c.done:
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > moonrakerMaxBackoff {
			backoff = moonrakerMaxBackoff
		}
	}
}

func (c *MoonrakerClient) markDisconnected() {
	c.mu.Lock()
	c.status.Connected = false
	c.status.KlippyReady = false
	c.mu.Unlock()
}

// connectAndServe runs one connection to completion, returning the error that
// ended it.
func (c *MoonrakerClient) connectAndServe() error {
	u := url.URL{Scheme: "ws", Host: c.host, Path: "/websocket"}

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", u.String(), err)
	}
	defer conn.Close()

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	conn.SetReadDeadline(time.Now().Add(moonrakerPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(moonrakerPongWait))
	})

	c.mu.Lock()
	c.status.Connected = true
	c.mu.Unlock()

	stop := make(chan struct{})
	defer close(stop)
	go c.keepalive(conn, stop)

	if err := c.subscribe(conn); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	return c.readLoop(conn)
}

// keepalive sends periodic pings. Without it a silent printer (idle, no status
// changes) would trip the read deadline and cause a pointless reconnect.
func (c *MoonrakerClient) keepalive(conn *websocket.Conn, stop <-chan struct{}) {
	ticker := time.NewTicker(moonrakerPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-c.done:
			return
		case <-ticker.C:
			c.connMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(moonrakerWriteWait))
			c.connMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (c *MoonrakerClient) nextRequestID() int {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	c.nextID++
	return c.nextID
}

// moonrakerSubscription is the object set we ask Klipper to stream. A nil field
// list means "all fields of this object".
func moonrakerSubscription() map[string]interface{} {
	return map[string]interface{}{
		"toolhead":       []string{"position", "extruder"},
		"print_stats":    []string{"state", "filename", "filament_used", "print_duration", "total_duration", "info"},
		"virtual_sdcard": []string{"progress"},
		"extruder":       []string{"temperature", "target"},
		"heater_bed":     []string{"temperature", "target"},
	}
}

// subscribe issues printer.objects.subscribe and seeds state from the reply.
// The reply carries a full snapshot, which is what establishes the tracker's
// baseline — billing must not start from a guess.
func (c *MoonrakerClient) subscribe(conn *websocket.Conn) error {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "printer.objects.subscribe",
		"params":  map[string]interface{}{"objects": moonrakerSubscription()},
		"id":      c.nextRequestID(),
	}

	c.connMu.Lock()
	conn.SetWriteDeadline(time.Now().Add(moonrakerWriteWait))
	err := conn.WriteJSON(req)
	c.connMu.Unlock()
	if err != nil {
		return err
	}

	// Read until the subscribe reply arrives; notifications may interleave.
	for {
		var msg moonrakerMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.Result != nil {
			var res struct {
				Status map[string]json.RawMessage `json:"status"`
			}
			if err := json.Unmarshal(msg.Result, &res); err != nil {
				return fmt.Errorf("decode subscribe result: %w", err)
			}
			c.applyStatus(res.Status, true)
			return nil
		}
		c.handleNotification(&msg)
	}
}

// moonrakerMessage is the subset of JSON-RPC 2.0 that Moonraker actually sends.
type moonrakerMessage struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	Result json.RawMessage   `json:"result"`
	ID     int               `json:"id"`
}

func (c *MoonrakerClient) readLoop(conn *websocket.Conn) error {
	for {
		select {
		case <-c.done:
			return nil
		default:
		}

		var msg moonrakerMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		c.handleNotification(&msg)
	}
}

func (c *MoonrakerClient) handleNotification(msg *moonrakerMessage) {
	switch msg.Method {
	case "notify_status_update":
		if len(msg.Params) == 0 {
			return
		}
		var objects map[string]json.RawMessage
		if err := json.Unmarshal(msg.Params[0], &objects); err != nil {
			return
		}
		c.applyStatus(objects, false)

	case "notify_klippy_ready":
		// Klipper restarted. The extruder axis may have moved or reset while it
		// was down, so the tracker must rebaseline from a fresh snapshot rather
		// than bill the gap. Marking it not-ready makes the next snapshot the
		// new baseline.
		c.mu.Lock()
		c.status.KlippyReady = true
		c.mu.Unlock()
		log.Printf("Moonraker %s: klippy ready — rebaselining extrusion tracker", c.host)

	case "notify_klippy_shutdown", "notify_klippy_disconnected":
		c.mu.Lock()
		c.status.KlippyReady = false
		c.mu.Unlock()
		log.Printf("Moonraker %s: klippy went away", c.host)
	}
}

// applyStatus folds one status payload into the cache and the tracker.
//
// isSnapshot marks the full state that arrives with a subscribe reply, which
// (re)establishes the tracker baseline instead of billing against a stale one.
func (c *MoonrakerClient) applyStatus(objects map[string]json.RawMessage, isSnapshot bool) {
	// --- toolhead: the tracker's input -------------------------------------
	if raw, ok := objects["toolhead"]; ok {
		var th struct {
			Position []float64 `json:"position"`
			Extruder string    `json:"extruder"`
		}
		if err := json.Unmarshal(raw, &th); err == nil {
			// position is [X, Y, Z, E]; index 3 is the raw kinematic extruder
			// axis. Anything shorter is not a position we can bill from.
			if len(th.Position) >= 4 {
				ePos := th.Position[3]
				if isSnapshot || !c.tracker.Ready() {
					c.tracker.Reset(ePos, th.Extruder)
					if c.debugLog {
						log.Printf("Moonraker %s: tracker baseline e=%.4f extruder=%q", c.host, ePos, th.Extruder)
					}
				} else {
					if used := c.tracker.HandleStatusUpdate(ePos, th.Extruder); used > 0 && c.debugLog {
						log.Printf("Moonraker %s: +%.4fmm (e=%.4f)", c.host, used, ePos)
					}
				}
			} else if th.Extruder != "" && !isSnapshot {
				// Extruder changed without a position in the same message.
				c.tracker.HandleStatusUpdate(c.currentEPos(), th.Extruder)
			}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if raw, ok := objects["print_stats"]; ok {
		var ps struct {
			State         *string  `json:"state"`
			Filename      *string  `json:"filename"`
			FilamentUsed  *float64 `json:"filament_used"`
			PrintDuration *float64 `json:"print_duration"`
			TotalDuration *float64 `json:"total_duration"`
			Info          *struct {
				CurrentLayer *int `json:"current_layer"`
				TotalLayer   *int `json:"total_layer"`
			} `json:"info"`
		}
		if err := json.Unmarshal(raw, &ps); err == nil {
			// Every field is a pointer because Moonraker sends partial updates:
			// an absent key means "unchanged", not "zero". Assigning eagerly
			// would blank the dashboard on every incremental message.
			if ps.State != nil {
				c.status.State = *ps.State
			}
			if ps.Filename != nil {
				c.status.Filename = *ps.Filename
			}
			if ps.FilamentUsed != nil {
				c.status.FilamentUsed = *ps.FilamentUsed
			}
			if ps.PrintDuration != nil {
				c.status.PrintDuration = *ps.PrintDuration
			}
			if ps.TotalDuration != nil {
				c.status.TotalDuration = *ps.TotalDuration
			}
			if ps.Info != nil {
				if ps.Info.CurrentLayer != nil {
					c.status.CurrentLayer = *ps.Info.CurrentLayer
				}
				if ps.Info.TotalLayer != nil {
					c.status.TotalLayer = *ps.Info.TotalLayer
				}
			}
		}
	}

	if raw, ok := objects["virtual_sdcard"]; ok {
		var vs struct {
			Progress *float64 `json:"progress"`
		}
		if err := json.Unmarshal(raw, &vs); err == nil && vs.Progress != nil {
			c.status.Progress = *vs.Progress
		}
	}

	if raw, ok := objects["extruder"]; ok {
		var h struct {
			Temperature *float64 `json:"temperature"`
			Target      *float64 `json:"target"`
		}
		if err := json.Unmarshal(raw, &h); err == nil {
			if h.Temperature != nil {
				c.status.NozzleTemp = *h.Temperature
			}
			if h.Target != nil {
				c.status.NozzleTarget = *h.Target
			}
		}
	}

	if raw, ok := objects["heater_bed"]; ok {
		var h struct {
			Temperature *float64 `json:"temperature"`
			Target      *float64 `json:"target"`
		}
		if err := json.Unmarshal(raw, &h); err == nil {
			if h.Temperature != nil {
				c.status.BedTemp = *h.Temperature
			}
			if h.Target != nil {
				c.status.BedTarget = *h.Target
			}
		}
	}

	if isSnapshot {
		c.status.KlippyReady = true
	}
	c.status.LastUpdate = time.Now()
}

// currentEPos reports the tracker's last seen extruder position, used when an
// extruder change arrives without an accompanying position.
func (c *MoonrakerClient) currentEPos() float64 {
	c.tracker.mu.Lock()
	defer c.tracker.mu.Unlock()
	return c.tracker.lastEPos
}
