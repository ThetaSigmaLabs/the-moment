// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Command line flags
	var (
		webOnly    = flag.Bool("web-only", false, "Run only the web interface")
		bridgeOnly = flag.Bool("bridge-only", false, "Run only the bridge service")
		port       = flag.String("port", DefaultWebPort, "Web interface port")
		host       = flag.String("host", "0.0.0.0", "Web interface host")
	)
	flag.Parse()

	// Create bridge instance first (with default config)
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		log.Fatalf("Failed to create bridge: %v", err)
	}
	defer bridge.Close()

	// Load configuration from database
	config, err := LoadConfig(bridge)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Update bridge with loaded config
	if err := bridge.UpdateConfig(config); err != nil {
		log.Fatalf("Failed to update bridge config: %v", err)
	}

	// Override port: env var (Docker/.env) takes priority, then DB config, then default
	if *port == DefaultWebPort {
		if envPort := os.Getenv("THE_MOMENT_PORT"); envPort != "" {
			*port = envPort
		} else if config.WebPort != DefaultWebPort {
			*port = config.WebPort
		}
	}

	// Override Spoolman URL: env var takes priority when the stored value is still
	// the hardcoded default (fresh install or never customised via UI).
	// This lets the Docker Compose SPOOLMAN_URL env var wire up the internal service
	// name (http://spoolman:8000) automatically without user action.
	if envSpoolman := os.Getenv("SPOOLMAN_URL"); envSpoolman != "" && config.SpoolmanURL == DefaultSpoolmanURL {
		config.SpoolmanURL = envSpoolman
		if err := bridge.SetConfigValue(ConfigKeySpoolmanURL, envSpoolman); err != nil {
			log.Printf("Warning: could not persist SPOOLMAN_URL env override: %v", err)
		}
		if err := bridge.UpdateConfig(config); err != nil {
			log.Printf("Warning: could not apply SPOOLMAN_URL env override to bridge: %v", err)
		}
		log.Printf("Spoolman URL set from SPOOLMAN_URL env var: %s", envSpoolman)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Auto-register Spoolman custom fields needed for NFC workflow
	go func() {
		time.Sleep(2 * time.Second) // let Spoolman finish starting
		created, existed, failed := bridge.spoolman.EnsureSpoolmanFields()
		if len(created) > 0 {
			log.Printf("NFC: created %d Spoolman custom field(s)", len(created))
		}
		if len(failed) > 0 {
			log.Printf("NFC: failed to create %d Spoolman custom field(s) — NFC tab will show setup warning", len(failed))
		}
		_ = existed
	}()

	// Start NFC session cleanup background task
	go func() {
		ticker := time.NewTicker(1 * time.Minute) // Clean up every minute
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := bridge.cleanupExpiredSessions(); err != nil {
					log.Printf("Error cleaning up NFC sessions: %v", err)
				}
			case <-sigChan:
				return
			}
		}
	}()

	// Drain the Spoolman outbox and G-code download queue every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := bridge.RetryPendingSpoolmanUpdates(); err != nil {
					log.Printf("Error retrying pending Spoolman updates: %v", err)
				}
				if err := bridge.RetryPendingGcodeDownloads(); err != nil {
					log.Printf("Error retrying pending G-code downloads: %v", err)
				}
			case <-sigChan:
				return
			}
		}
	}()

	if *webOnly {
		// Run only web interface
		fmt.Println("Starting web interface only...")
		webServer := NewWebServer(bridge)
		go func() {
			if err := webServer.Start(*host, *port); err != nil {
				log.Fatalf("Web server error: %v", err)
			}
		}()

		// Wait for shutdown signal
		<-sigChan
		fmt.Println("Shutting down web server...")

	} else if *bridgeOnly {
		// Run only bridge service
		fmt.Println("Starting bridge service only...")
		fmt.Printf("Monitoring printers: %v\n", getPrinterNames(config))
		fmt.Printf("Spoolman URL: %s\n", config.SpoolmanURL)
		fmt.Printf("Poll interval: %v\n", config.PollInterval)

		// Start monitoring in a goroutine
		go func() {
			ticker := time.NewTicker(config.PollInterval)
			defer ticker.Stop()

			// Run initial check
			bridge.MonitorPrinters()

			// Continue monitoring
			for {
				select {
				case <-ticker.C:
					bridge.MonitorPrinters()
				case <-sigChan:
					return
				}
			}
		}()

		// Wait for shutdown signal
		<-sigChan
		fmt.Println("Shutting down bridge service...")

	} else {
		// Run both bridge service and web interface
		fmt.Println("Starting both bridge service and web interface...")
		fmt.Printf("Monitoring printers: %v\n", getPrinterNames(config))
		fmt.Printf("Spoolman URL: %s\n", config.SpoolmanURL)
		fmt.Printf("Poll interval: %v\n", config.PollInterval)
		fmt.Printf("Web interface: http://%s:%s\n", *host, *port)

		// Create web server first so we can pass it to monitoring
		webServer := NewWebServer(bridge)

		// Start bridge monitoring in a goroutine
		go func() {
			ticker := time.NewTicker(config.PollInterval)
			defer ticker.Stop()

			locationSyncTicker := time.NewTicker(config.LocationSyncInterval)
			defer locationSyncTicker.Stop()

			// Run initial check
			bridge.MonitorPrinters()
			// Broadcast initial status
			webServer.BroadcastStatus()

			// Continue monitoring
			for {
				select {
				case <-ticker.C:
					bridge.MonitorPrinters()
					// Broadcast status after each monitoring cycle
					webServer.BroadcastStatus()
				case <-locationSyncTicker.C:
					if changed, err := bridge.SyncSpoolmanLocationsToDB(); err != nil {
						log.Printf("Spoolman location sync error: %v", err)
					} else if changed {
						webServer.BroadcastStatus()
					}
				case <-sigChan:
					return
				}
			}
		}()

		// Start web server in a goroutine
		go func() {
			if err := webServer.Start(*host, *port); err != nil {
				log.Fatalf("Web server error: %v", err)
			}
		}()

		// Wait for shutdown signal
		<-sigChan
		fmt.Println("Shutting down services...")
	}
}

// getPrinterNames returns a slice of printer names from config
func getPrinterNames(config *Config) []string {
	names := make([]string, 0, len(config.Printers))
	for name := range config.Printers {
		names = append(names, name)
	}
	return names
}
