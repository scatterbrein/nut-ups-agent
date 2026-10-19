package pairing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
)

func Claim(ctx context.Context, c config.Config, base, code, agentID string) error {
	if c.Backend.URL != "" {
		return errors.New("remove manual backend settings before pairing this agent")
	}
	base = strings.TrimRight(base, "/")
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if !regexp.MustCompile(`^[0-9A-F]{16}$`).MatchString(code) {
		return errors.New("pairing code must contain 16 hexadecimal characters")
	}
	test := c
	test.Backend = config.Backend{URL: base, TokenFile: filepath.Join(c.StateDir, "placeholder.secret"), AllowHTTPLoopback: c.Backend.AllowHTTPLoopback}
	if err := test.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"code": code, "agent_id": agentID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/pairings/claim", bytes.NewReader(b))
	if err != nil {
		return errors.New("invalid pairing URL")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("could not contact pairing service securely")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("pairing was not accepted (HTTP %d); obtain a new code if the previous request may have completed", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return errors.New("invalid pairing receipt")
	}
	var result struct {
		AgentID    string `json:"agent_id"`
		AgentToken string `json:"agent_token"`
		Endpoint   string `json:"endpoint"`
	}
	if json.Unmarshal(body, &result) != nil || result.AgentID != agentID || result.Endpoint != base+"/ingest" || !regexp.MustCompile(`^ups_agent_[A-Za-z0-9_-]{43}$`).MatchString(result.AgentToken) {
		return errors.New("pairing receipt does not match this agent or service")
	}
	old := c
	_ = old.LoadPairedConnection()
	tokenPath := filepath.Join(c.StateDir, "backend-token-"+model.NewID()+".secret")
	if err = atomicWrite(tokenPath, []byte(result.AgentToken+"\n")); err != nil {
		return err
	}
	connection, _ := json.Marshal(config.PairedConnection{URL: result.Endpoint, TokenFile: tokenPath})
	if err = atomicWrite(filepath.Join(c.StateDir, "backend-connection.json"), connection); err != nil {
		os.Remove(tokenPath)
		return err
	}
	if old.Backend.TokenFile != tokenPath && config.IsPairedTokenPath(c.StateDir, old.Backend.TokenFile) {
		_ = os.Remove(old.Backend.TokenFile)
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pairing-*")
	if err != nil {
		return errors.New("cannot write private pairing settings")
	}
	name := f.Name()
	defer os.Remove(name)
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
		return errors.New("cannot save private pairing settings")
	}
	if os.Rename(name, path) != nil {
		return errors.New("cannot commit private pairing settings")
	}
	d, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	if err != nil {
		return errors.New("cannot sync pairing settings; obtain a new code if necessary")
	}
	return nil
}
