// Package onboarding links a fresh installation with a phone using a short
// installation code. The LAN listener exposes no secret or mutation operation.
package onboarding

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
	"omstudioit.com/nut-ups-agent/internal/pairing"
)

const ServiceURL = "https://ups.omstudioit.com/api/v1"
const DiscoveryPort = "3494"
const hello = "DISCOVER UPSMONITOR 1\n"

type Installation struct {
	ExpiresAt  int64  `json:"-"`
	ID         string `json:"id"`
	AgentID    string `json:"agent_id"`
	Name       string `json:"name"`
	Proof      string `json:"proof"`
	Code       string `json:"code"`
	AgentToken string `json:"agent_token"`
}
type Discovery struct {
	Service        string `json:"service"`
	Version        int    `json:"version"`
	InstallationID string `json:"installation_id"`
	AgentID        string `json:"agent_id"`
	NUTPorts       []int  `json:"nut_ports"`
}

func path(c config.Config) string { return filepath.Join(c.StateDir, "installation.secret") }
func Load(c config.Config) (Installation, error) {
	var s Installation
	raw, err := config.ReadSecret(path(c))
	if err != nil {
		return s, err
	}
	if json.Unmarshal([]byte(raw), &s) != nil || !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(s.ID) || !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(s.AgentID) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s.Proof) || !regexp.MustCompile(`^[0-9]{8}$`).MatchString(s.Code) || !regexp.MustCompile(`^ups_agent_[A-Za-z0-9_-]{43}$`).MatchString(s.AgentToken) {
		return s, errors.New("invalid saved installation")
	}
	var expiry struct {
		ExpiresAt int64 `json:"expires_at"`
	}
	_ = json.Unmarshal([]byte(raw), &expiry)
	s.ExpiresAt = expiry.ExpiresAt
	return s, nil
}
func Prepare(ctx context.Context, c config.Config, agentID string) (Installation, error) {
	return prepare(ctx, c, agentID, ServiceURL)
}
func prepare(ctx context.Context, c config.Config, agentID, base string) (Installation, error) {
	pairedToken := ""
	if c.Backend.URL != "" {
		if c.Backend.URL != ServiceURL+"/ingest" {
			return Installation{}, errors.New("custom backend preserved; use its existing pairing flow")
		}
		var err error
		pairedToken, err = config.ReadSecret(c.Backend.TokenFile)
		if err != nil {
			return Installation{}, err
		}
	}
	s, err := Load(c)
	if pairedToken != "" {
		s = Installation{}
		err = os.ErrNotExist
	}
	if err != nil {
		if _, statErr := os.Stat(path(c)); statErr == nil && pairedToken == "" {
			return s, err
		}
		s = Installation{ID: model.NewID(), AgentID: agentID, Name: "My UPS server"}
		if len(c.Sources) > 0 && c.Sources[0].Name != "" {
			s.Name = c.Sources[0].Name
			if len(s.Name) > 80 {
				s.Name = "My UPS server"
			}
		}
		secret := make([]byte, 32)
		if _, err = rand.Read(secret); err != nil {
			return s, err
		}
		s.Proof = hex.EncodeToString(secret)
		if _, err = rand.Read(secret); err != nil {
			return s, err
		}
		s.AgentToken = "ups_agent_" + base64.RawURLEncoding.EncodeToString(secret)
		if pairedToken != "" {
			s.AgentToken = pairedToken
		}
		n, err := rand.Int(rand.Reader, big.NewInt(100000000))
		if err != nil {
			return s, err
		}
		s.Code = fmt.Sprintf("%08d", n)
		if err = save(c, s); err != nil {
			return s, err
		}
	}
	if s.AgentID != agentID {
		return s, errors.New("installation identity differs from agent state")
	}
	var response struct {
		ID      string `json:"id"`
		Expires int64  `json:"expires_at"`
	}
	err = request(ctx, c, base, "/installations", s, &response)
	// Expired unclaimed installations may be renewed explicitly by the operator.
	if err != nil && strings.Contains(err.Error(), "HTTP 410") {
		if removeErr := os.Remove(path(c)); removeErr != nil {
			return s, removeErr
		}
		return prepare(ctx, c, agentID, base)
	}
	if err == nil && (response.ID != s.ID || response.Expires <= time.Now().Unix() || response.Expires > time.Now().Unix()+1860) {
		return s, errors.New("invalid installation receipt or server clock")
	}
	if err == nil {
		s.ExpiresAt = response.Expires
		err = save(c, s)
	}
	return s, err
}
func save(c config.Config, s Installation) error {
	data, err := json.Marshal(struct {
		Installation
		ExpiresAt int64 `json:"expires_at"`
	}{s, s.ExpiresAt})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.StateDir, ".installation-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path(c))
}
func request(ctx context.Context, c config.Config, base, path string, body, result any) error {
	// Only the compiled HTTPS service is used in production. Loopback is test-only.
	if base != ServiceURL && !c.Backend.AllowHTTPLoopback {
		return errors.New("invalid onboarding service")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("cannot reach remote setup service")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("remote setup failed (HTTP %d)", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 16385))
	if err != nil || len(b) > 16384 || json.Unmarshal(b, result) != nil {
		return errors.New("invalid remote setup response")
	}
	return nil
}
func privatePeer(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}
func handle(conn net.Conn, discovery Discovery) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if !privatePeer(conn.RemoteAddr().String()) {
		return
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 128)).ReadString('\n')
	if err != nil || line != hello {
		return
	}
	_ = json.NewEncoder(conn).Encode(discovery)
}

// Run returns true after remote activation. Failure/expiry never stops NUT polling.
func Run(ctx context.Context, c config.Config, agentID string) (bool, error) {
	return run(ctx, c, agentID, ServiceURL, ":"+DiscoveryPort, 10*time.Second)
}
func run(ctx context.Context, c config.Config, agentID, base, address string, interval time.Duration) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s, err := Load(c)
	if err != nil {
		return false, nil
	}
	if s.ExpiresAt <= time.Now().Unix() {
		return false, nil
	}
	if s.AgentID != agentID {
		return false, errors.New("installation identity mismatch")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return false, errors.New("local agent discovery port unavailable")
	}
	defer listener.Close()
	discovery := Discovery{Service: "nut-ups-monitor-agent", Version: 1, InstallationID: s.ID, AgentID: agentID}
	for _, source := range c.Sources {
		discovery.NUTPorts = append(discovery.NUTPorts, source.Port)
	}
	go func() { <-ctx.Done(); listener.Close() }()
	slots := make(chan struct{}, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func() { defer func() { <-slots }(); handle(conn, discovery) }()
			default:
				conn.Close()
			}
		}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if time.Now().Unix() >= s.ExpiresAt {
			return false, nil
		}
		if c.Backend.URL != "" {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		var status struct {
			Linked  bool   `json:"linked"`
			AgentID string `json:"agent_id"`
		}
		err = request(ctx, c, base, "/installations/status", map[string]string{"id": s.ID, "proof": s.Proof}, &status)
		if err == nil && status.Linked {
			if status.AgentID != agentID {
				return false, errors.New("invalid linked agent identity")
			}
			if err = pairing.SaveConnection(c, ServiceURL+"/ingest", s.AgentToken); err != nil {
				return false, err
			}
			return true, nil
		}
		if err != nil && (strings.Contains(err.Error(), "HTTP 410") || strings.Contains(err.Error(), "HTTP 401")) {
			return false, errors.New("installation code expired; run setup again")
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}
