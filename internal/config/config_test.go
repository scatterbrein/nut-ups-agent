package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{StateDir: "/tmp/agent", PollSeconds: 3, NUTTimeoutSeconds: 5, FailureThreshold: 2, LowBatteryPercent: 10, VoltageDropPercent: 15, MaxPending: 100, Sources: []Source{{ID: "pi", Host: "127.0.0.1", Port: 3493}}}
}
func TestValidationAndSecrets(t *testing.T) {
	for name, modify := range map[string]func(*Config){
		"HTTP remote": func(c *Config) {
			c.Backend = Backend{URL: "http://example.com/ingest", TokenFile: "/tmp/token", AllowHTTPLoopback: true}
		},
		"URL secret": func(c *Config) {
			c.Backend = Backend{URL: "https://user:secret@example.com/ingest", TokenFile: "/tmp/token"}
		},
		"query token": func(c *Config) {
			c.Backend = Backend{URL: "https://example.com/ingest?token=secret", TokenFile: "/tmp/token"}
		},
		"partial credentials": func(c *Config) { c.Sources[0].Username = "upsapp" },
		"duplicate source":    func(c *Config) { c.Sources = append(c.Sources, c.Sources[0]) },
		"command injection":   func(c *Config) { c.Sources[0].UPSKeys = []string{"ups\nINSTCMD"} },
		"no sources":          func(c *Config) { c.Sources = nil },
		"relative state":      func(c *Config) { c.StateDir = "state" },
		"negative upload":     func(c *Config) { c.UploadSeconds = -1 },
		"stale heartbeat":     func(c *Config) { c.UploadSeconds = 31 },
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			modify(&c)
			if c.Validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	c := validConfig()
	c.Sources[0].Port = 0
	c.Backend = Backend{URL: "http://127.0.0.1:1234/ingest", TokenFile: "/tmp/token", AllowHTTPLoopback: true}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Sources[0].Port != 3493 {
		t.Fatal("port default missing")
	}
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(" private password \n"), 0600); err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecret(path)
	if err != nil || secret != " private password " {
		t.Fatal("secret whitespace changed")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadSecret(path); err == nil {
		t.Fatal("world-readable secret accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("secret\nPASSWORD injected"), 0600)
	if _, err = ReadSecret(path); err == nil || strings.Contains(err.Error(), "injected") {
		t.Fatal("unsafe secret accepted or leaked")
	}
}
func TestLoadStrictAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{`{"state_dir":"/tmp/test","sources":[{"id":"pi","host":"127.0.0.1"}],"unknown":true}`, `{} {}`} {
		os.WriteFile(path, []byte(body), 0600)
		if _, err := Load(path); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	os.WriteFile(path, []byte(`{"state_dir":"/tmp/test","sources":[{"id":"pi","host":"127.0.0.1"}]}`), 0600)
	c, err := Load(path)
	if err != nil || c.PollSeconds != 3 || c.UploadSeconds != 30 || c.UploadInterval() != 30*time.Second || c.Sources[0].Port != 3493 {
		t.Fatal(c, err)
	}
}

func TestNonRegularSecretCannotBlockPolling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecret(path); err == nil {
		t.Fatal("FIFO accepted as a secret file")
	}
}

func TestNonRegularPairedSettingsCannotBlockStartup(t *testing.T) {
	c := validConfig()
	c.StateDir = t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(c.StateDir, "backend-connection.json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadPairedConnection(); err == nil {
		t.Fatal("FIFO accepted as paired settings")
	}
}
