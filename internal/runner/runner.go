package runner

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/nut"
	"omstudioit.com/nut-ups-agent/internal/state"
	"omstudioit.com/nut-ups-agent/internal/uplink"
)

type Runner struct {
	Config          config.Config
	Store           *state.Store
	Log             *slog.Logger
	lastWarning     time.Time
	lastDropped     uint64
	lastDropWarning time.Time
}

func (r *Runner) Poll(ctx context.Context) error {
	results := make([]nut.Result, len(r.Config.Sources))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for i, source := range r.Config.Sources {
		wg.Add(1)
		go func(i int, s config.Source) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-limit }()
			results[i] = (nut.Client{Timeout: time.Duration(r.Config.NUTTimeoutSeconds) * time.Second}).Poll(ctx, s)
		}(i, source)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := r.Store.Record(results, r.Config, time.Now()); err != nil {
		return err
	}
	readings := 0
	failures := 0
	for _, result := range results {
		readings += len(result.Variables)
		if result.ErrorCode != "" || len(result.FailedUPS) > 0 {
			failures++
		}
	}
	if failures > 0 {
		if r.lastWarning.IsZero() || time.Since(r.lastWarning) >= time.Minute {
			r.Log.Warn("NUT readings incomplete; preserving last known values", "sources_affected", failures)
			r.lastWarning = time.Now()
		}
	} else {
		if !r.lastWarning.IsZero() {
			r.Log.Info("NUT readings available again")
			r.lastWarning = time.Time{}
		}
		r.Log.Debug("UPS readings saved", "devices", readings)
	}
	_, dropped := r.Store.Stats()
	if dropped != r.lastDropped {
		if r.lastDropWarning.IsZero() || time.Since(r.lastDropWarning) >= time.Minute {
			r.Log.Warn("Local backlog reached its limit; some messages were discarded", "dropped_messages_total", dropped)
			r.lastDropWarning = time.Now()
		}
		r.lastDropped = dropped
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, once bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake := make(chan struct{}, 1)
	fatal := make(chan error, 1)
	var wg sync.WaitGroup
	if !once && r.Config.Backend.URL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.upload(ctx, wake); err != nil {
				fatal <- err
				cancel()
			}
		}()
	}
	defer wg.Wait()
	// Cancel before waiting for the uploader, including on local persistence failures.
	defer cancel()
	for {
		if err := r.Poll(ctx); err != nil {
			select {
			case uploadError := <-fatal:
				return uploadError
			default:
			}
			return err
		}
		if once {
			if r.Config.Backend.URL != "" {
				return r.sendOnce(ctx)
			}
			return nil
		}
		select {
		case wake <- struct{}{}:
		default:
		}
		timer := time.NewTimer(r.Config.PollInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			select {
			case err := <-fatal:
				return err
			default:
				return ctx.Err()
			}
		case <-timer.C:
		}
	}
}
func (r *Runner) sendOnce(ctx context.Context) error {
	sender := uplink.New(r.Config.Backend)
	batch := r.Store.Batch()
	ids, err := sender.Send(ctx, batch)
	if err != nil {
		return err
	}
	return r.Store.Acknowledge(ids)
}
func (r *Runner) upload(ctx context.Context, wake <-chan struct{}) error {
	sender := uplink.New(r.Config.Backend)
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		batch := r.Store.Batch()
		if len(batch) == 0 {
			select {
			case <-wake:
				continue
			case <-ctx.Done():
				return nil
			}
		}
		ids, err := sender.Send(ctx, batch)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			if err = r.Store.Acknowledge(ids); err != nil {
				return err
			}
			failures = 0
			r.Log.Debug("Backend acknowledged UPS readings", "messages", len(ids))
			continue
		}
		failures++
		delay := uplink.Backoff(failures, err)
		r.Log.Warn("Upload postponed; readings remain on this device", "reason", err.Error(), "retry_seconds", int(delay.Seconds()))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
