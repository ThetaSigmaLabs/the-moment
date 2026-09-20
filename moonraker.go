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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
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

	// Live telemetry for the printer detail view.
	AxisZ     float64 // mm, current layer height
	Flow      int     // % — Klipper's extrude_factor
	Speed     int     // % — Klipper's speed_factor
	FanPrint  int     // % — part cooling fan
	FanHotend int     // % — hotend heatbreak fan

	LastUpdate time.Time
}

// MoonrakerStatusProvider is the seam the dashboard and tests depend on,
// mirroring BambuStatusProvider so a fake can stand in for a live printer.
type MoonrakerStatusProvider interface {
	GetCurrentStatus() (MoonrakerStatus, error)
	Tracker() *ExtrusionTracker
	// ActiveSpoolID reports the spool Moonraker's [spoolman] component has
	// active. ErrMoonrakerNoSpoolman means the component is not configured.
	ActiveSpoolID() (int, error)
	// SetActiveSpoolID assigns the spool in Moonraker, which is what Mainsail
	// and Fluidd display.
	SetActiveSpoolID(spoolID int) error
	// LastCompletedJob returns Moonraker's own record for a finished job, which
	// is the authoritative per-print consumption. Nil means no match.
	LastCompletedJob(filename string) (*MoonrakerJob, error)
	Close()
}

// ErrMoonrakerNoSpoolman means the printer has no [spoolman] section, so
// Moonraker holds no spool assignment and The Moment's own mapping is
// authoritative.
var ErrMoonrakerNoSpoolman = errors.New("moonraker: spoolman component not configured")

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

	// httpClient serves Moonraker's REST endpoints. The spool assignment lives
	// behind /server/spoolman/* rather than the status subscription, so it
	// cannot come over the WebSocket without interleaving request/reply
	// plumbing into the read loop.
	httpClient *http.Client
}

var _ MoonrakerStatusProvider = (*MoonrakerClient)(nil)

// NewMoonrakerClient creates a client and starts its connection loop. The
// caller owns the returned client and must Close it.
func NewMoonrakerClient(host string, debugLog bool) *MoonrakerClient {
	c := &MoonrakerClient{
		host:       normalizeMoonrakerHost(host),
		tracker:    NewExtrusionTracker(),
		done:       make(chan struct{}),
		debugLog:   debugLog,
		httpClient: &http.Client{Timeout: 10 * time.Second},
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

// ActiveSpoolID reports the spool Moonraker's [spoolman] component has active.
//
// Moonraker is treated as the source of truth for Klipper printers because
// Mainsail and Fluidd read and write this same value, and those UIs are what
// people have open while a print runs. Mirroring it here means the spool can be
// set from either place without the two diverging.
//
// Returns ErrMoonrakerNoSpoolman when the component is absent, and 0 when it is
// present but no spool is assigned.
func (c *MoonrakerClient) ActiveSpoolID() (int, error) {
	var body struct {
		Result struct {
			SpoolID *int `json:"spool_id"`
		} `json:"result"`
	}
	if err := c.getJSON("/server/spoolman/spool_id", &body); err != nil {
		return 0, err
	}
	if body.Result.SpoolID == nil {
		return 0, nil // component present, no spool assigned
	}
	return *body.Result.SpoolID, nil
}

// SetActiveSpoolID assigns the spool in Moonraker so Mainsail and Fluidd show
// it, and so Moonraker's own [spoolman] bills the right spool.
func (c *MoonrakerClient) SetActiveSpoolID(spoolID int) error {
	payload, err := json.Marshal(map[string]int{"spool_id": spoolID})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s/server/spoolman/spool_id", c.host), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrMoonrakerNoSpoolman
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("moonraker set spool_id: HTTP %d", resp.StatusCode)
	}
	return nil
}

// MoonrakerJob is one entry from Moonraker's own print history.
type MoonrakerJob struct {
	JobID    string
	Filename string
	Status   string
	// FilamentUsed is Moonraker's own figure for the job, in millimetres. It is
	// the same number Spoolman was debited, so it is what print history should
	// record.
	FilamentUsed float64
	// SlicerFilament is the slicer's estimate from the file's metadata, also in
	// millimetres. The difference against FilamentUsed is the purge and
	// tip-shaping overhead the slicer never sees.
	SlicerFilament float64
}

// LastCompletedJob returns the most recent finished job matching filename.
//
// Print history takes its figure from here rather than from our own tracker.
// The tracker's high-water mark is in-memory: restarting The Moment rebaselines
// it to wherever the axis happens to be, and if that is below the previous
// high-water — which it is after any tip-shaping retraction — the next print
// re-bills the re-prime. Measured on real hardware, that inflated one print's
// recorded usage by 49mm (3.9%). Moonraker's figure has no such state.
//
// Returns nil when no matching finished job is found, which the caller treats
// as "fall back to the tracker".
func (c *MoonrakerClient) LastCompletedJob(filename string) (*MoonrakerJob, error) {
	var body struct {
		Result struct {
			Jobs []struct {
				JobID        string  `json:"job_id"`
				Filename     string  `json:"filename"`
				Status       string  `json:"status"`
				FilamentUsed float64 `json:"filament_used"`
				Metadata     struct {
					FilamentTotal float64 `json:"filament_total"`
				} `json:"metadata"`
			} `json:"jobs"`
		} `json:"result"`
	}
	// A handful is plenty: the job of interest has just finished, and asking for
	// the whole history on every print end would be wasteful.
	if err := c.getJSON("/server/history/list?limit=10", &body); err != nil {
		return nil, err
	}

	for _, j := range body.Result.Jobs {
		if j.Filename != filename || j.Status == "in_progress" {
			continue
		}
		return &MoonrakerJob{
			JobID:          j.JobID,
			Filename:       j.Filename,
			Status:         j.Status,
			FilamentUsed:   j.FilamentUsed,
			SlicerFilament: j.Metadata.FilamentTotal,
		}, nil
	}
	return nil, nil
}

// getJSON performs a GET against Moonraker's HTTP API and decodes the body.
func (c *MoonrakerClient) getJSON(path string, out interface{}) error {
	resp, err := c.httpClient.Get(fmt.Sprintf("http://%s%s", c.host, path))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Moonraker answers 404 for endpoints belonging to components that are not
	// configured, which is how an absent [spoolman] presents.
	if resp.StatusCode == http.StatusNotFound {
		return ErrMoonrakerNoSpoolman
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("moonraker GET %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

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

	// Ask whether Klipper is up before subscribing. Moonraker answers either
	// way, so this is the only way to distinguish a ready printer from one
	// whose firmware is shut down.
	if err := c.queryKlippyState(conn); err != nil {
		return fmt.Errorf("server.info: %w", err)
	}

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

		// Display-only. gcode_move is subscribed for the flow/speed overrides
		// and the displayed Z height — NEVER for extrusion. Its gcode_position
		// E axis is rewritten by every slicer G92 E0 (observed live: raw axis
		// 596.46mm vs gcode_position 7.23mm on the same move), so billing from
		// it would be meaningless. Extrusion comes from toolhead.position only.
		"gcode_move":     []string{"speed_factor", "extrude_factor", "gcode_position"},
		"display_status": []string{"progress"},
		"fan":            []string{"speed"},
		// Config-dependent: most Klipper configs name the heatbreak fan this
		// way, but it is optional and absence is handled.
		"heater_fan hotend_fan": []string{"speed"},
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
		// A JSON-RPC error reply is terminal for this request. Without this the
		// loop would keep reading until the 90s read deadline expired.
		if msg.Error != nil {
			return fmt.Errorf("subscribe rejected: %s", string(msg.Error))
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

// queryKlippyState asks Moonraker whether Klipper is actually up.
//
// Moonraker answers the WebSocket even when Klipper is shut down or in an error
// state, and it only emits notify_klippy_ready on a *transition*. So a client
// that connects while Klipper is already down would otherwise never learn it —
// which is why readiness has to be asked for explicitly at connect time rather
// than inferred from the arrival of a status snapshot.
func (c *MoonrakerClient) queryKlippyState(conn *websocket.Conn) error {
	id := c.nextRequestID()
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "server.info",
		"id":      id,
	}

	c.connMu.Lock()
	conn.SetWriteDeadline(time.Now().Add(moonrakerWriteWait))
	err := conn.WriteJSON(req)
	c.connMu.Unlock()
	if err != nil {
		return err
	}

	for {
		var msg moonrakerMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.ID != id {
			// Not our reply — a notification, or a reply to something else.
			if msg.Result == nil && msg.Error == nil {
				c.handleNotification(&msg)
			}
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("server.info failed: %s", string(msg.Error))
		}
		var info struct {
			KlippyState string `json:"klippy_state"`
		}
		if err := json.Unmarshal(msg.Result, &info); err != nil {
			return fmt.Errorf("decode server.info: %w", err)
		}

		ready := info.KlippyState == "ready"
		c.mu.Lock()
		c.status.KlippyReady = ready
		c.mu.Unlock()
		if !ready {
			log.Printf("Moonraker %s: connected, but klippy_state=%q — not tracking extrusion",
				c.host, info.KlippyState)
		}
		return nil
	}
}

// moonrakerMessage is the subset of JSON-RPC 2.0 that Moonraker actually sends.
type moonrakerMessage struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	Result json.RawMessage   `json:"result"`
	Error  json.RawMessage   `json:"error"`
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

	// display_status carries the slicer's own M73 estimate, which accounts for
	// time rather than bytes consumed and so tracks what the printer's screen
	// and Mainsail show. It arrives after virtual_sdcard here deliberately: when
	// both are present the M73 figure wins.
	if raw, ok := objects["display_status"]; ok {
		var ds struct {
			Progress *float64 `json:"progress"`
		}
		if err := json.Unmarshal(raw, &ds); err == nil && ds.Progress != nil {
			c.status.Progress = *ds.Progress
		}
	}

	// gcode_move: display fields only. gcode_position[3] is the G92-rewritten
	// extruder axis and is deliberately not read — extrusion comes from
	// toolhead.position above.
	if raw, ok := objects["gcode_move"]; ok {
		var gm struct {
			SpeedFactor   *float64  `json:"speed_factor"`
			ExtrudeFactor *float64  `json:"extrude_factor"`
			GcodePosition []float64 `json:"gcode_position"`
		}
		if err := json.Unmarshal(raw, &gm); err == nil {
			if gm.SpeedFactor != nil {
				c.status.Speed = int(*gm.SpeedFactor * 100)
			}
			if gm.ExtrudeFactor != nil {
				c.status.Flow = int(*gm.ExtrudeFactor * 100)
			}
			// Index 2 is Z. Safe to read: unlike the E axis, Z is not rewritten
			// in a way that matters for display.
			if len(gm.GcodePosition) >= 3 {
				c.status.AxisZ = gm.GcodePosition[2]
			}
		}
	}

	if raw, ok := objects["fan"]; ok {
		var f struct {
			Speed *float64 `json:"speed"`
		}
		if err := json.Unmarshal(raw, &f); err == nil && f.Speed != nil {
			c.status.FanPrint = int(*f.Speed * 100)
		}
	}

	// Optional: not every Klipper config defines a hotend fan under this name.
	if raw, ok := objects["heater_fan hotend_fan"]; ok {
		var f struct {
			Speed *float64 `json:"speed"`
		}
		if err := json.Unmarshal(raw, &f); err == nil && f.Speed != nil {
			c.status.FanHotend = int(*f.Speed * 100)
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

	// Readiness is deliberately NOT inferred from a snapshot arriving.
	// Moonraker serves status even while Klipper is shut down, so treating any
	// snapshot as proof of readiness would report a dead printer as idle.
	// queryKlippyState establishes it at connect; notifications maintain it.
	c.status.LastUpdate = time.Now()
}

// currentEPos reports the tracker's last seen extruder position, used when an
// extruder change arrives without an accompanying position.
func (c *MoonrakerClient) currentEPos() float64 {
	c.tracker.mu.Lock()
	defer c.tracker.mu.Unlock()
	return c.tracker.lastEPos
}
