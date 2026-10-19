package pairing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
)

func settings(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	return config.Config{StateDir: dir, PollSeconds: 3, NUTTimeoutSeconds: 5, FailureThreshold: 2, LowBatteryPercent: 10, VoltageDropPercent: 15, MaxPending: 100, Sources: []config.Source{{ID: "pi", Host: "127.0.0.1", Port: 3493}}, Backend: config.Backend{AllowHTTPLoopback: true}}
}
func TestPairingPersistsSeparateTokenAndRotatesSafely(t *testing.T) {
	c := settings(t)
	id := model.NewID()
	token := "ups_agent_" + strings.Repeat("a", 43)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]string
		if json.NewDecoder(r.Body).Decode(&input) != nil || input["agent_id"] != id || input["code"] != "0123456789ABCDEF" || r.URL.Path != "/api/v1/pairings/claim" {
			t.Error("invalid pairing request")
		}
		json.NewEncoder(w).Encode(map[string]string{"agent_id": id, "agent_token": token, "endpoint": server.URL + "/api/v1/ingest"})
	}))
	defer server.Close()
	if err := Claim(context.Background(), c, server.URL+"/api/v1", "0123-4567-89ab-cdef", id); err != nil {
		t.Fatal(err)
	}
	loaded := c
	if err := loaded.LoadPairedConnection(); err != nil {
		t.Fatal(err)
	}
	first := loaded.Backend.TokenFile
	b, _ := os.ReadFile(filepath.Join(c.StateDir, "backend-connection.json"))
	if strings.Contains(string(b), token) {
		t.Fatal("raw token stored in settings JSON")
	}
	secret, err := config.ReadSecret(first)
	if err != nil || secret != token {
		t.Fatal("private token not available")
	}
	if err := Claim(context.Background(), c, server.URL+"/api/v1", "0123456789ABCDEF", id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("obsolete token file retained")
	}
}
func TestPairingRejectsUnsafeTransportRedirectAndReceipt(t *testing.T) {
	for _, mode := range []string{"redirect", "wrong endpoint", "wrong agent", "invalid token", "too large"} {
		t.Run(mode, func(t *testing.T) {
			c := settings(t)
			id := model.NewID()
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, "https://example.invalid", 307)
					return
				}
				if mode == "too large" {
					fmt.Fprint(w, strings.Repeat("x", 65537))
					return
				}
				body := map[string]string{"agent_id": id, "endpoint": server.URL + "/ingest", "agent_token": "ups_agent_" + strings.Repeat("a", 43)}
				if mode == "wrong endpoint" {
					body["endpoint"] = "https://example.invalid/ingest"
				}
				if mode == "wrong agent" {
					body["agent_id"] = model.NewID()
				}
				if mode == "invalid token" {
					body["agent_token"] = "bad"
				}
				json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			if err := Claim(context.Background(), c, server.URL, "0123456789ABCDEF", id); err == nil {
				t.Fatal("unsafe receipt accepted")
			}
			if _, err := os.Stat(filepath.Join(c.StateDir, "backend-connection.json")); !os.IsNotExist(err) {
				t.Fatal("invalid pairing changed saved connection")
			}
		})
	}
	c := settings(t)
	c.Backend.AllowHTTPLoopback = false
	if err := Claim(context.Background(), c, "http://127.0.0.1:1", "0123456789ABCDEF", model.NewID()); err == nil {
		t.Fatal("HTTP enabled implicitly")
	}
}
