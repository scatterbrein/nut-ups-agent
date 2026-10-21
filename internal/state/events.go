package state

import (
	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
	"time"
)

type Device struct {
	Reading          model.Reading `json:"reading"`
	Stale            bool          `json:"stale"`
	OnBattery        bool          `json:"on_battery"`
	LowBattery       bool          `json:"low_battery"`
	VoltageLow       bool          `json:"voltage_low"`
	VoltageReference *float64      `json:"voltage_reference,omitempty"`
}
type Source struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	LastAttempt    time.Time  `json:"last_attempt"`
	LastSuccess    *time.Time `json:"last_success,omitempty"`
	Failures       int        `json:"failures"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
	FirstFailureAt *time.Time `json:"first_failure_at,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
}

func event(kind, source, device string, at time.Time) model.Event {
	return model.Event{ID: model.NewID(), Kind: kind, SourceID: source, DeviceID: device, ObservedAt: at}
}

func observe(old Device, reading model.Reading, c config.Config) (Device, []model.Event) {
	d := old
	var events []model.Event
	uncertain := old.Reading.DeviceID == "" || old.Stale || reading.ObservedAt.Sub(old.Reading.ObservedAt) > 2*c.PollInterval()+time.Duration(c.NUTTimeoutSeconds)*time.Second
	add := func(kind string) {
		e := event(kind, reading.SourceID, reading.DeviceID, reading.ObservedAt)
		e.TimingUncertain = uncertain
		events = append(events, e)
	}
	if reading.Flag("OB") {
		if !d.OnBattery {
			add("on_battery")
		}
		d.OnBattery = true
	} else if reading.Flag("OL") {
		if d.OnBattery {
			add("power_restored")
		}
		d.OnBattery = false
	}
	charge := reading.Number("battery.charge")
	if charge != nil && *charge > 100 {
		charge = nil
	}
	threshold := c.LowBatteryPercent
	if v := reading.Number("battery.charge.low"); v != nil && *v <= 100 {
		threshold = *v
	}
	// Reported charge is the only low-battery alert criterion. NUT LB may also
	// describe a runtime reserve while the battery remains fully charged.
	low := charge != nil && *charge <= threshold
	if low && !d.LowBattery {
		add("low_battery")
		events[len(events)-1].BatteryCharge = charge
		events[len(events)-1].ChargeThreshold = &threshold
	}
	if low {
		d.LowBattery = true
	} else if charge != nil && *charge >= min(100, threshold+2) {
		d.LowBattery = false
	}
	if nominal := reading.Number("input.voltage.nominal"); nominal != nil && *nominal > 0 {
		d.VoltageReference = nominal
	}
	v := reading.Number("input.voltage")
	if d.VoltageReference == nil && v != nil && *v > 0 && reading.Flag("OL") {
		d.VoltageReference = v
	}
	if v != nil && d.VoltageReference != nil {
		limit := *d.VoltageReference * (1 - c.VoltageDropPercent/100)
		if *v < limit && !d.VoltageLow {
			add("voltage_drop")
			d.VoltageLow = true
		}
		if *v >= limit+*d.VoltageReference*0.02 {
			d.VoltageLow = false
		}
	}
	d.Reading = reading
	d.Stale = false
	return d, events
}
