// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPushoverClient_Send_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":1}`))
	}))
	defer srv.Close()

	client := NewPushoverClient("testtoken", "testuserkey")
	client.httpClient.Transport = redirectTransport(srv.URL)

	if err := client.Send("Test Title", "Test message", 0); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestPushoverClient_Send_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"status":0,"errors":["token is invalid"]}`))
	}))
	defer srv.Close()

	client := NewPushoverClient("badtoken", "testuserkey")
	client.httpClient.Transport = redirectTransport(srv.URL)

	err := client.Send("", "msg", 0)
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
}

func TestPushoverClient_Send_NoCredentials(t *testing.T) {
	// Should fail immediately without making any HTTP call
	client := NewPushoverClient("", "")
	err := client.Send("Title", "Message", 0)
	if err == nil {
		t.Fatal("expected error for empty credentials, got nil")
	}
}

func TestPushoverClient_Send_EmptyToken(t *testing.T) {
	client := NewPushoverClient("", "someuserkey")
	err := client.Send("Title", "Message", 0)
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

func TestPushoverClient_PriorityClamp(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Errorf("could not parse form: %v", err)
		}
		priority := r.FormValue("priority")
		if priority != "1" {
			t.Errorf("expected priority clamped to 1, got %s", priority)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":1}`))
	}))
	defer srv.Close()

	client := NewPushoverClient("token", "userkey")
	client.httpClient.Transport = redirectTransport(srv.URL)

	// Priority 5 should be clamped to 1
	if err := client.Send("", "msg", 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call, got %d", calls)
	}
}

// redirectTransport returns an http.RoundTripper that rewrites all requests to baseURL.
func redirectTransport(baseURL string) http.RoundTripper {
	return &rewriteTransport{base: baseURL}
}

type rewriteTransport struct {
	base string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	req2.URL.Host = strings.TrimPrefix(t.base, "http://")
	return http.DefaultTransport.RoundTrip(req2)
}
