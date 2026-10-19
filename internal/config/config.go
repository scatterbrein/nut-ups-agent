package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Source struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	UPSKeys      []string `json:"ups_keys,omitempty"`
	Username     string   `json:"username,omitempty"`
	PasswordFile string   `json:"password_file,omitempty"`
}

type Backend struct {
	URL               string `json:"url"`
	TokenFile         string `json:"token_file,omitempty"`
	AllowHTTPLoopback bool   `json:"allow_http_loopback,omitempty"`
}

type Config struct {
	StateDir           string   `json:"state_dir"`
	PollSeconds        int      `json:"poll_seconds"`
	NUTTimeoutSeconds  int      `json:"nut_timeout_seconds"`
	FailureThreshold   int      `json:"failure_threshold"`
	LowBatteryPercent  float64  `json:"low_battery_percent"`
	VoltageDropPercent float64  `json:"voltage_drop_percent"`
	MaxPending         int      `json:"max_pending_messages"`
	Sources            []Source `json:"sources"`
	Backend            Backend  `json:"backend"`
}

func Load(path string) (Config, error) {
	c := Config{PollSeconds: 3, NUTTimeoutSeconds: 5, FailureThreshold: 2, LowBatteryPercent: 10, VoltageDropPercent: 15, MaxPending: 4096}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return c, errors.New("cannot open agent configuration")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128*1024 {
		return c, errors.New("agent configuration must be a regular JSON file of at most 128 KiB")
	}
	d := json.NewDecoder(io.LimitReader(f, 128*1024))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, errors.New("invalid agent configuration JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("configuration must contain one JSON object")
	}
	return c, c.Validate()
}

func (c *Config) Validate() error {
	if !filepath.IsAbs(c.StateDir) {
		return errors.New("state_dir must be an absolute directory")
	}
	if c.PollSeconds < 1 || c.PollSeconds > 3600 || c.NUTTimeoutSeconds < 1 || c.NUTTimeoutSeconds > 60 || c.FailureThreshold < 1 || c.FailureThreshold > 20 {
		return errors.New("invalid polling, timeout or failure threshold")
	}
	if !(c.LowBatteryPercent > 0 && c.LowBatteryPercent <= 100) || !(c.VoltageDropPercent > 0 && c.VoltageDropPercent <= 80) {
		return errors.New("invalid battery or voltage threshold")
	}
	if c.MaxPending < 10 || c.MaxPending > 20000 {
		return errors.New("max_pending_messages must be between 10 and 20000")
	}
	if len(c.Sources) == 0 || len(c.Sources) > 16 {
		return errors.New("configure between 1 and 16 NUT sources")
	}
	seen := map[string]bool{}
	for i := range c.Sources {
		s := &c.Sources[i]
		s.Host = strings.TrimSpace(s.Host)
		if s.ID == "" || len(s.ID) > 80 || strings.ContainsAny(s.ID, "\r\n\x00") || seen[s.ID] {
			return errors.New("source IDs must be nonempty and unique")
		}
		seen[s.ID] = true
		if s.Host == "" || strings.ContainsAny(s.Host, " /\\\r\n\x00") || strings.Contains(s.Host, ":") && net.ParseIP(s.Host) == nil {
			return errors.New("invalid NUT host")
		}
		if s.Port == 0 {
			s.Port = 3493
		}
		if s.Port < 1 || s.Port > 65535 || len(s.Name) > 200 || len(s.UPSKeys) > 64 {
			return errors.New("invalid NUT port, name or UPS selection")
		}
		if (s.Username == "") != (s.PasswordFile == "") || strings.ContainsAny(s.Username, "\r\n\x00") {
			return errors.New("NUT authentication requires a username and password_file together")
		}
		if s.PasswordFile != "" && !filepath.IsAbs(s.PasswordFile) {
			return errors.New("password_file must be absolute")
		}
		keys := map[string]bool{}
		for _, key := range s.UPSKeys {
			if key == "" || len(key) > 200 || strings.ContainsAny(key, "\r\n\x00") || keys[key] {
				return errors.New("invalid or repeated UPS key")
			}
			keys[key] = true
		}
	}
	if c.Backend.URL == "" {
		if c.Backend.TokenFile != "" {
			return errors.New("backend token_file requires a backend URL")
		}
		return nil
	}
	u, err := url.Parse(c.Backend.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("backend URL must not contain credentials, a query or a fragment")
	}
	loop := net.ParseIP(u.Hostname())
	local := loop != nil && loop.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local && c.Backend.AllowHTTPLoopback) {
		return errors.New("backend requires HTTPS; HTTP is allowed only for explicitly enabled loopback tests")
	}
	if !filepath.IsAbs(c.Backend.TokenFile) {
		return errors.New("backend requires an absolute token_file")
	}
	return nil
}

func (c Config) PollInterval() time.Duration { return time.Duration(c.PollSeconds) * time.Second }

// Files are separate from the JSON configuration, and their contents never enter telemetry.
func ReadSecret(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("cannot read secret file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 && st.Mode().Perm() != 0640 {
		return "", errors.New("secret file must be regular and restricted to mode 0600 or 0640")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return "", errors.New("secret file is unreadable or too large")
	}
	s := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if s == "" || strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("secret file must contain a single nonempty line")
	}
	return s, nil
}

func (s Source) Address() string { return net.JoinHostPort(s.Host, fmt.Sprint(s.Port)) }
