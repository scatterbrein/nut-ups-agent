package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

type PairedConnection struct {
	URL       string `json:"url"`
	TokenFile string `json:"token_file"`
}

func IsPairedTokenPath(dir, path string) bool {
	return filepath.Dir(path) == filepath.Clean(dir) && regexp.MustCompile(`^backend-token-[0-9a-f-]{36}\.secret$`).MatchString(filepath.Base(path))
}

// Explicit configuration takes precedence. Pairing stores only a reference to a separate private token file.
func (c *Config) LoadPairedConnection() error {
	if c.Backend.URL != "" {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(c.StateDir, "backend-connection.json"), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read paired backend settings")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 || info.Mode().Perm() != 0600 {
		return errors.New("paired backend settings must be a private regular file")
	}
	var connection PairedConnection
	d := json.NewDecoder(io.LimitReader(f, 8193))
	d.DisallowUnknownFields()
	if d.Decode(&connection) != nil || d.Decode(new(any)) != io.EOF || !IsPairedTokenPath(c.StateDir, connection.TokenFile) {
		return errors.New("invalid paired backend settings")
	}
	c.Backend = Backend{URL: connection.URL, TokenFile: connection.TokenFile, AllowHTTPLoopback: c.Backend.AllowHTTPLoopback}
	return c.Validate()
}
