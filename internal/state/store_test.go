package state

import (
	"encoding/json"
	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
	"omstudioit.com/nut-ups-agent/internal/nut"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func settings(dir string) config.Config {
	os.Chmod(dir, 0700)
	return config.Config{StateDir: dir, PollSeconds: 3, NUTTimeoutSeconds: 5, FailureThreshold: 2, LowBatteryPercent: 10, VoltageDropPercent: 15, MaxPending: 10, Sources: []config.Source{{ID: "pi", Name: "Office", Host: "127.0.0.1", Port: 3493, Username: "upsapp", PasswordFile: "/private/password"}}}
}
func result(c config.Config, status, charge, voltage string) nut.Result {
	return nut.Result{Source: c.Sources[0], Reachable: true, Devices: []string{"ups_office"}, Variables: map[string]map[string]string{"ups_office": {"ups.status": status, "battery.charge": charge, "input.voltage": voltage, "input.voltage.nominal": "120", "ups.load": "5", "ups.realpower.nominal": "810", "driver.password": "DO_NOT_PERSIST"}}}
}
func count(d Data, kind string) int {
	n := 0
	for _, e := range d.RecentEvents {
		if e.Kind == kind {
			n++
		}
	}
	return n
}
func record(t *testing.T, s *Store, r nut.Result, c config.Config, at time.Time) {
	t.Helper()
	if err := s.Record([]nut.Result{r}, c, at); err != nil {
		t.Fatal(err)
	}
}

func TestOutagePersistenceFrozenReadingsAndRecovery(t *testing.T) {
	dir := t.TempDir()
	c := settings(dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	record(t, s, result(c, "OL", "100", "125"), c, at)
	lost := nut.Result{Source: c.Sources[0], ErrorCode: "unavailable"}
	record(t, s, lost, c, at.Add(3*time.Second))
	d, _ := Read(dir)
	if count(d, "nut_server_unavailable") != 0 {
		t.Fatal("single timeout incorrectly confirmed an outage")
	}
	device := d.Devices[model.DeviceID(model.SourceID(c.Sources[0]), "ups_office")]
	if !device.Stale || !device.Reading.ObservedAt.Equal(at) || device.Reading.Variables["battery.charge"] != "100" {
		t.Fatal("last reading erased or changed")
	}
	record(t, s, lost, c, at.Add(6*time.Second))
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	record(t, s, lost, c, at.Add(time.Minute))
	record(t, s, result(c, "OL", "98", "124"), c, at.Add(14*time.Minute))
	d, _ = Read(dir)
	if count(d, "nut_server_unavailable") != 1 || count(d, "nut_server_reconnected") != 1 {
		t.Fatal("repeated connectivity events", d.RecentEvents)
	}
	for _, e := range d.RecentEvents {
		if e.Kind == "nut_server_reconnected" && (*e.DisconnectedSeconds != 837) {
			t.Fatal("outage duration incorrect", *e.DisconnectedSeconds)
		}
	}
	device = d.Devices[device.Reading.DeviceID]
	if device.Stale || device.Reading.Variables["battery.charge"] != "98" {
		t.Fatal("fresh mode not restored")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	journal, _ := os.ReadFile(filepath.Join(dir, "journal.bin"))
	b = append(b, journal...)
	for _, secret := range []string{"DO_NOT_PERSIST", "upsapp", "/private/password"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("credentials persisted")
		}
	}
	st, _ := os.Stat(filepath.Join(dir, "state.json"))
	if st.Mode().Perm() != 0600 {
		t.Fatal("state permissions")
	}
}
func TestEnergyEventsDeduplicateAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	c := settings(dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	record(t, s, result(c, "OL", "100", "125"), c, at)
	record(t, s, result(c, "OB", "87", "0"), c, at.Add(3*time.Second))
	record(t, s, result(c, "OB LB", "9", "0"), c, at.Add(6*time.Second))
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	record(t, s, result(c, "OB LB", "9", "0"), c, at.Add(9*time.Second))
	record(t, s, result(c, "OL", "9", "125"), c, at.Add(12*time.Second))
	d, _ := Read(dir)
	for _, kind := range []string{"on_battery", "power_restored", "low_battery", "voltage_drop"} {
		if count(d, kind) != 1 {
			t.Fatal(kind, d.RecentEvents)
		}
	}
}
func TestBoundedQueuePreservesEventMessagesAndIdentity(t *testing.T) {
	dir := t.TempDir()
	c := settings(dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now().UTC()
	record(t, s, result(c, "OB", "87", "0"), c, at)
	original, _ := json.Marshal(s.Batch()[0])
	firstID := s.Batch()[0].ID
	for i := 1; i < 20; i++ {
		record(t, s, result(c, "OB", "87", "0"), c, at.Add(time.Duration(i)*3*time.Second))
	}
	d, _ := Read(dir)
	if len(d.Pending) != 10 || d.DroppedMessages != 10 {
		t.Fatal("queue did not remain bounded")
	}
	if d.Pending[0].ID != firstID {
		t.Fatal("outage event was discarded before ordinary telemetry")
	}
	now, _ := json.Marshal(d.Pending[0])
	if string(now) != string(original) {
		t.Fatal("retry payload mutated")
	}
	if err = s.Acknowledge([]string{firstID}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, e := range s.Batch() {
		if e.ID == firstID {
			t.Fatal("acknowledged record returned after restart")
		}
	}
}
func TestIsolationMissingDeviceReadErrorsAndCorruption(t *testing.T) {
	dir := t.TempDir()
	c := settings(dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, e := Open(dir); e == nil {
		second.Close()
		t.Fatal("duplicate process acquired state")
	}
	at := time.Now()
	record(t, s, result(c, "OL", "100", "125"), c, at)
	readError := nut.Result{Source: c.Sources[0], ErrorCode: "authentication_failed"}
	record(t, s, readError, c, at.Add(3*time.Second))
	record(t, s, readError, c, at.Add(6*time.Second))
	d, _ := Read(dir)
	if count(d, "nut_server_unavailable") != 0 {
		t.Fatal("authentication failure described as unreachable server")
	}
	missing := nut.Result{Source: c.Sources[0], Reachable: true, Devices: []string{}, Variables: map[string]map[string]string{}}
	record(t, s, missing, c, at.Add(9*time.Second))
	d, _ = Read(dir)
	for _, device := range d.Devices {
		if !device.Stale {
			t.Fatal("removed device shown live")
		}
	}
	c.Sources[0].Host = "other.local"
	record(t, s, result(c, "OL", "50", "120"), c, at.Add(12*time.Second))
	d, _ = Read(dir)
	if len(d.Devices) != 1 {
		t.Fatal("previous endpoint retained as current")
	}
	for _, v := range d.Devices {
		if v.Reading.SourceID != model.SourceID(c.Sources[0]) {
			t.Fatal("wrong endpoint")
		}
	}
	s.Close()
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte("{broken"), 0600)
	if s, e := Open(dir); e == nil {
		s.Close()
		t.Fatal("corrupted state silently reset")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "{broken" {
		t.Fatal("corrupted state was overwritten")
	}
}
