package onboarding

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
	"omstudioit.com/nut-ups-agent/internal/pairing"
)

func fixture(t *testing.T) (config.Config, Installation) {
	t.Helper()
	c := config.Config{StateDir: t.TempDir(), PollSeconds: 3, NUTTimeoutSeconds: 5, FailureThreshold: 2, LowBatteryPercent: 10, VoltageDropPercent: 15, MaxPending: 100, Sources: []config.Source{{ID: "local", Name: "Local", Host: "127.0.0.1", Port: 3493}}}
	s := Installation{ID: model.NewID(), AgentID: model.NewID(), Name: "Local", Proof: strings.Repeat("a", 64), Code: "00123456", AgentToken: "ups_agent_" + strings.Repeat("b", 43), ExpiresAt: time.Now().Add(time.Minute).Unix()}
	return c, s
}
func TestPrivateSavedInstallationSurvivesRestart(t *testing.T) {
	c, s := fixture(t)
	if err := save(c, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(c)
	if err != nil || got != s {
		t.Fatalf("installation lost: %v", err)
	}
	info, _ := os.Stat(path(c))
	if info.Mode().Perm() != 0600 {
		t.Fatal("installation not private")
	}
	_, err = Prepare(context.Background(), config.Config{Backend: config.Backend{URL: "https://example.invalid"}}, s.AgentID)
	if err == nil {
		t.Fatal("paired install could be replaced")
	}
}
func TestDiscoveryOnlyAnswersFixedHandshakeAndLeaksNoSecrets(t *testing.T) {
	_, s := fixture(t)
	d := Discovery{Service: "nut-ups-monitor-agent", Version: 1, InstallationID: s.ID, AgentID: s.AgentID, NUTPorts: []int{3493}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handle(conn, d)
		}
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(hello))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var got Discovery
	if json.Unmarshal([]byte(line), &got) != nil || got.InstallationID != s.ID {
		t.Fatal("invalid discovery")
	}
	for _, secret := range []string{s.Code, s.Proof, s.AgentToken} {
		if strings.Contains(line, secret) {
			t.Fatal("discovery leaked a secret")
		}
	}
	other, _ := net.Dial("tcp", listener.Addr().String())
	defer other.Close()
	other.Write([]byte("GET / HTTP/1.1\r\n"))
	other.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = bufio.NewReader(other).ReadByte(); err == nil {
		t.Fatal("browser request received discovery")
	}
}
func TestPeerFilterAndNoStateWithoutSetup(t *testing.T) {
	for _, peer := range []string{"192.168.1.2:123", "127.0.0.1:5", "[fe80::1]:123"} {
		if !privatePeer(peer) {
			t.Fatal("private peer rejected")
		}
	}
	for _, peer := range []string{"8.8.8.8:123", "invalid", "[2001:4860:4860::8888]:22"} {
		if privatePeer(peer) {
			t.Fatal("public peer accepted")
		}
	}
	c, s := fixture(t)
	linked, err := Run(context.Background(), c, s.AgentID)
	if linked || err != nil {
		t.Fatal("missing optional setup affects local agent")
	}
}
func TestReceiptRejectsRedirectsAndCommitsExistingConnectionFormat(t *testing.T) {
	c, s := fixture(t)
	c.Backend.AllowHTTPLoopback = true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://127.0.0.1/other", 302) }))
	defer server.Close()
	var response any
	if err := request(context.Background(), c, server.URL, "/installations", s, &response); err == nil {
		t.Fatal("redirect followed")
	}
	c.Backend.AllowHTTPLoopback = false
	if err := request(context.Background(), c, server.URL, "/installations", s, &response); err == nil {
		t.Fatal("insecure service accepted")
	}
	if err := pairing.SaveConnection(c, ServiceURL+"/ingest", s.AgentToken); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadPairedConnection(); err != nil {
		t.Fatal(err)
	}
	if c.Backend.URL != ServiceURL+"/ingest" {
		t.Fatal("connection unavailable")
	}
	if secret, err := config.ReadSecret(c.Backend.TokenFile); err != nil || secret != s.AgentToken {
		t.Fatal("token not durable")
	}
	if filepath.Dir(c.Backend.TokenFile) != c.StateDir {
		t.Fatal("token outside private state")
	}
}
func TestSuccessfulActivationCommitsConnectionAndReturnsWithoutAffectingNUTConfig(t *testing.T) {
	c, s := fixture(t)
	c.Backend.AllowHTTPLoopback = true
	if err := save(c, s); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["proof"] != s.Proof || body["id"] != s.ID {
			t.Error("wrong private status proof")
		}
		json.NewEncoder(w).Encode(map[string]any{"linked": true, "agent_id": s.AgentID})
	}))
	defer server.Close()
	original := c.Sources[0]
	linked, err := run(context.Background(), c, s.AgentID, server.URL, "127.0.0.1:0", time.Millisecond)
	if err != nil || !linked {
		t.Fatalf("activation failed: %v", err)
	}
	if err = c.LoadPairedConnection(); err != nil {
		t.Fatal(err)
	}
	if c.Sources[0].Host != original.Host || c.Sources[0].ID != original.ID || c.Sources[0].Port != original.Port {
		t.Fatal("setup changed NUT connection")
	}
}
func TestOperatorRecoveryReusesPrivateAgentTokenAndKeepsNUTSettings(t *testing.T) {
	c, s := fixture(t)
	if err := pairing.SaveConnection(c, ServiceURL+"/ingest", s.AgentToken); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadPairedConnection(); err != nil {
		t.Fatal(err)
	}
	c.Backend.AllowHTTPLoopback = true
	before := c.Backend
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body Installation
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid synthetic body", 400)
			return
		}
		if body.AgentToken != s.AgentToken || body.AgentID != s.AgentID {
			t.Error("recovery rotated existing credentials")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": body.ID, "expires_at": time.Now().Add(30 * time.Minute).Unix()})
	}))
	defer server.Close()
	got, err := prepare(context.Background(), c, s.AgentID, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code == "" || got.AgentToken != s.AgentToken || c.Backend != before {
		t.Fatal("recovery altered current link")
	}
	loaded, err := Load(c)
	if err != nil || loaded.ID != got.ID {
		t.Fatal("recovery code not durable")
	}
}
