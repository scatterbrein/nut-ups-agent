package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"omstudioit.com/nut-ups-agent/internal/config"
)

// Only these readings may cross the agent boundary. No arbitrary NUT strings, credentials or serial numbers.
var allowed = strings.Fields("ups.status battery.charge battery.charge.low battery.charge.warning ups.load battery.runtime battery.voltage battery.voltage.nominal input.voltage input.voltage.nominal output.voltage ups.realpower ups.realpower.nominal device.mfr device.model ups.mfr ups.model ups.temperature")

type Reading struct {
	DeviceID       string            `json:"device_id"`
	SourceID       string            `json:"source_id"`
	UPSKey         string            `json:"ups_key"`
	ObservedAt     time.Time         `json:"observed_at"`
	Variables      map[string]string `json:"variables"`
	PowerWatts     *float64          `json:"power_watts,omitempty"`
	PowerEstimated bool              `json:"power_estimated"`
}

type Event struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	SourceID            string    `json:"source_id"`
	DeviceID            string    `json:"device_id,omitempty"`
	ObservedAt          time.Time `json:"observed_at"`
	DisconnectedSeconds *float64  `json:"disconnected_seconds,omitempty"`
	TimingUncertain     bool      `json:"timing_uncertain,omitempty"`
}

type SourceObservation struct {
	SourceID       string   `json:"source_id"`
	Name           string   `json:"name,omitempty"`
	Connection     string   `json:"connection"`
	ErrorCode      string   `json:"error_code,omitempty"`
	DiscoveredUPS  []string `json:"discovered_ups"`
	UnavailableUPS []string `json:"unavailable_ups,omitempty"`
}

type Envelope struct {
	SchemaVersion   int                 `json:"schema_version"`
	AgentID         string              `json:"agent_id"`
	ID              string              `json:"id"`
	Sequence        uint64              `json:"sequence"`
	RecordedAt      time.Time           `json:"recorded_at"`
	Sources         []SourceObservation `json:"sources"`
	Readings        []Reading           `json:"readings"`
	LastKnown       []Reading           `json:"last_known_readings,omitempty"`
	Events          []Event             `json:"events"`
	DroppedMessages uint64              `json:"dropped_messages_total"`
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system random source unavailable")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func Hash(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func SourceID(s config.Source) string {
	return Hash(s.ID, strings.ToLower(s.Host), strconv.Itoa(s.Port))
}
func DeviceID(sourceID, key string) string { return Hash(sourceID, key) }

func NewReading(sourceID, key string, variables map[string]string, at time.Time) Reading {
	r := Reading{DeviceID: DeviceID(sourceID, key), SourceID: sourceID, UPSKey: key, ObservedAt: at.UTC(), Variables: map[string]string{}}
	for _, name := range allowed {
		if v, ok := variables[name]; ok && len(v) <= 256 && !strings.ContainsAny(v, "\x00\r\n") {
			r.Variables[name] = v
		}
	}
	if w := r.Number("ups.realpower"); w != nil {
		r.PowerWatts = w
	} else if n, l := r.Number("ups.realpower.nominal"), r.Number("ups.load"); n != nil && l != nil && *n > 0 && *l <= 100 {
		w := *n * *l / 100
		if !math.IsInf(w, 0) {
			r.PowerWatts = &w
			r.PowerEstimated = true
		}
	}
	return r
}
func (r Reading) Number(name string) *float64 {
	n, err := strconv.ParseFloat(r.Variables[name], 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return nil
	}
	return &n
}
func (r Reading) Flag(flag string) bool {
	for _, s := range strings.Fields(r.Variables["ups.status"]) {
		if s == flag {
			return true
		}
	}
	return false
}
