package model

import (
	"encoding/json"
	"omstudioit.com/nut-ups-agent/internal/config"
	"strings"
	"testing"
	"time"
)

func TestReferenceReadingsAndPrivacy(t *testing.T) {
	r := NewReading("source", "ups_office", map[string]string{"ups.status": "OL", "battery.charge": "100", "ups.load": "5", "ups.realpower.nominal": "810", "battery.runtime": "11430", "input.voltage": "125.0", "battery.voltage": "24.0", "driver.password": "SECRET", "username": "upsapp", "device.serial": "PRIVATE_SERIAL"}, time.Now())
	if r.PowerWatts == nil || *r.PowerWatts != 40.5 || !r.PowerEstimated {
		t.Fatal("reference estimate incorrect", r)
	}
	if r.Number("output.voltage") != nil {
		t.Fatal("missing metric fabricated")
	}
	b, _ := json.Marshal(r)
	for _, secret := range []string{"SECRET", "upsapp", "PRIVATE_SERIAL"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("private data leaked")
		}
	}
	r.Variables["ups.realpower"] = "36.25"
	measured := NewReading("source", "ups_office", r.Variables, time.Now())
	if measured.PowerEstimated || measured.PowerWatts == nil || *measured.PowerWatts != 36.25 {
		t.Fatal("measured power must take priority")
	}
	for _, invalid := range []string{"NaN", "+Inf", "-1", "unknown"} {
		r.Variables["battery.charge"] = invalid
		if r.Number("battery.charge") != nil {
			t.Fatal("invalid metric accepted")
		}
	}
}
func TestEndpointAndUPSIsolation(t *testing.T) {
	s := config.Source{ID: "pi", Host: "HOST.local", Port: 3493}
	base := SourceID(s)
	s.Host = "host.local"
	if SourceID(s) != base {
		t.Fatal("host case should normalize")
	}
	s.Port = 3494
	if SourceID(s) == base {
		t.Fatal("port mixed")
	}
	s.Port = 3493
	s.Host = "other.local"
	if SourceID(s) == base {
		t.Fatal("host mixed")
	}
	if DeviceID(base, "ups1") == DeviceID(base, "ups2") {
		t.Fatal("UPS keys mixed")
	}
}
