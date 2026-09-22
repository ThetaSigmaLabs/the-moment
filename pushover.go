// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const pushoverAPIURL = "https://api.pushover.net/1/messages.json"

// PushoverClient sends push notifications via the Pushover API.
// API reference: https://pushover.net/api
type PushoverClient struct {
	APIToken   string
	UserKey    string
	httpClient *http.Client
}

// NewPushoverClient returns a PushoverClient ready to call the Pushover API.
func NewPushoverClient(apiToken, userKey string) *PushoverClient {
	return &PushoverClient{
		APIToken: apiToken,
		UserKey:  userKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Send posts a notification to Pushover. title may be empty (Pushover uses the
// app name as the default). priority: -1=low, 0=normal, 1=high. Values above 1
// are clamped to 1 — emergency priority (2) requires retry/expire params not used here.
func (p *PushoverClient) Send(title, message string, priority int) error {
	if p.APIToken == "" || p.UserKey == "" {
		return fmt.Errorf("pushover: API token or user key not configured")
	}
	if priority > 1 {
		priority = 1
	}
	if priority < -2 {
		priority = -2
	}

	form := url.Values{
		"token":    {p.APIToken},
		"user":     {p.UserKey},
		"message":  {message},
		"priority": {fmt.Sprintf("%d", priority)},
	}
	if title != "" {
		form.Set("title", title)
	}

	resp, err := p.httpClient.Post(
		pushoverAPIURL,
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return fmt.Errorf("pushover: HTTP error: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pushover: API returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
