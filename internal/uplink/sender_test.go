package uplink

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func token(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("private-agent-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func messages() []model.Envelope {
	return []model.Envelope{{SchemaVersion: 1, AgentID: "agent-one", ID: "message-one", Sequence: 1, RecordedAt: time.Now().UTC()}, {SchemaVersion: 1, AgentID: "agent-one", ID: "message-two", Sequence: 2, RecordedAt: time.Now().UTC()}}
}

func TestExplicitAcknowledgementAndRetryIdentity(t *testing.T) {
	var bodies []string
	var keys []string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer private-agent-token" {
			t.Error("authentication or method missing")
		}
		var request Request
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid request")
		}
		b, _ := json.Marshal(request)
		bodies = append(bodies, string(b))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		calls++
		if strings.Contains(string(b), "private-agent-token") {
			t.Error("token copied into telemetry")
		}
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"acknowledged_ids":["message-one"]}`)
	}))
	defer server.Close()
	s := New(config.Backend{URL: server.URL, TokenFile: token(t), AllowHTTPLoopback: true})
	batch := messages()
	if _, err := s.Send(context.Background(), batch); err == nil {
		t.Fatal("failed delivery acknowledged")
	}
	ids, err := s.Send(context.Background(), batch)
	if err != nil || len(ids) != 1 || ids[0] != "message-one" {
		t.Fatal(ids, err)
	}
	if bodies[0] != bodies[1] || keys[0] == "" || keys[0] != keys[1] {
		t.Fatal("retry identity changed")
	}
}
func TestMissingInvalidAndOversizedReceiptsNeverAcknowledge(t *testing.T) {
	for name, receipt := range map[string]string{"empty": "", "empty IDs": `{"acknowledged_ids":[]}`, "unknown ID": `{"acknowledged_ids":["other"]}`, "repeated ID": `{"acknowledged_ids":["message-one","message-one"]}`, "too large": strings.Repeat("x", 65537)} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, receipt) }))
			defer server.Close()
			s := New(config.Backend{URL: server.URL, TokenFile: token(t)})
			if ids, err := s.Send(context.Background(), messages()); err == nil || len(ids) != 0 {
				t.Fatal("invalid receipt acknowledged")
			}
		})
	}
}
func TestRedirectNeverReceivesToken(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	s := New(config.Backend{URL: origin.URL, TokenFile: token(t)})
	if _, err := s.Send(context.Background(), messages()); err == nil {
		t.Fatal("redirect accepted")
	}
	if redirected {
		t.Fatal("token sent to redirected endpoint")
	}
}
func TestTLSCertificateIsVerified(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"acknowledged_ids":["message-one"]}`) }))
	defer server.Close()
	s := New(config.Backend{URL: server.URL, TokenFile: token(t)})
	if _, err := s.Send(context.Background(), messages()); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	// A trusted test CA succeeds. Production exposes no TLS-verification override.
	s.client = server.Client()
	ids, err := s.Send(context.Background(), messages())
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
}
func TestRetryDelaysAndRedactedErrors(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{{"120", 120 * time.Second}, {"999999999999", 24 * time.Hour}, {"-1", 0}, {"bad", 0}} {
		if got := retryAfter(test.value, time.Now()); got != test.want {
			t.Fatal(test.value, got)
		}
	}
	if got := Backoff(1, &RetryError{429, 20 * time.Minute}); got < 20*time.Minute {
		t.Fatal("Retry-After ignored")
	}
	if got := Backoff(1, &RetryError{401, 0}); got < 5*time.Minute {
		t.Fatal("authentication failure retried too quickly")
	}
	s := New(config.Backend{URL: "http://127.0.0.1:1/secret-path", TokenFile: token(t)})
	_, err := s.Send(context.Background(), messages())
	if err == nil || strings.Contains(err.Error(), "secret-path") || strings.Contains(err.Error(), "private-agent-token") {
		t.Fatal("transport error disclosed sensitive URL or token")
	}
}
