package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/nut"
	"omstudioit.com/nut-ups-agent/internal/state"
	"omstudioit.com/nut-ups-agent/internal/uplink"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUploadIntervalBatchesReadingsAndEventsBypassTheWait(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	token := filepath.Join(dir, "token")
	os.WriteFile(token, []byte("synthetic-test-token"), 0600)
	received := make(chan uplink.Request, 10)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request uplink.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var ids []string
		for _, message := range request.Messages {
			ids = append(ids, message.ID)
		}
		json.NewEncoder(w).Encode(uplink.Receipt{AcknowledgedIDs: ids})
		received <- request
	}))
	defer backend.Close()
	c := config.Config{StateDir: dir, PollSeconds: 3, UploadSeconds: 30, NUTTimeoutSeconds: 2, FailureThreshold: 2, LowBatteryPercent: 10, VoltageDropPercent: 15, MaxPending: 100, Sources: []config.Source{{ID: "pi", Host: "127.0.0.1", Port: 3493}}, Backend: config.Backend{URL: backend.URL, TokenFile: token, AllowHTTPLoopback: true}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	add := func(status string) {
		t.Helper()
		if err := s.Record([]nut.Result{{Source: c.Sources[0], Reachable: true, Devices: []string{"office"}, Variables: map[string]map[string]string{"office": {"ups.status": status, "battery.charge": "87"}}}}, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	next := func() uplink.Request {
		t.Helper()
		select {
		case request := <-received:
			return request
		case <-time.After(2 * time.Second):
			t.Fatal("missing upload")
			return uplink.Request{}
		}
	}
	acknowledged := func() {
		t.Helper()
		for i := 0; i < 200; i++ {
			if count, _ := s.Stats(); count == 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("receipt not saved")
	}
	quiet := func() {
		t.Helper()
		select {
		case <-received:
			t.Fatal("ordinary reading uploaded before the periodic deadline")
		case <-time.After(60 * time.Millisecond):
		}
	}
	add("OL")
	r := Runner{Config: c, Store: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), uploadEvery: 300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- r.upload(ctx, wake) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("uploader did not stop")
		}
	}()
	if first := next(); len(first.Messages) != 1 {
		t.Fatal("first reading was not sent immediately")
	}
	acknowledged()
	add("OL")
	add("OL")
	wake <- struct{}{} // A stale wake must not bypass the routine interval.
	quiet()
	if batch := next(); len(batch.Messages) != 2 || batch.Messages[0].Sequence != 2 || batch.Messages[1].Sequence != 3 {
		t.Fatal("routine readings were not delivered together in order")
	}
	acknowledged()
	add("OB")
	wake <- struct{}{}
	select {
	case event := <-received:
		if len(event.Messages) != 1 || len(event.Messages[0].Events) != 1 || event.Messages[0].Events[0].Kind != "on_battery" {
			t.Fatal("event was not preserved")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("event waited for the periodic deadline")
	}
	acknowledged()
	add("OB")
	wake <- struct{}{}
	quiet()
}

func TestNUTToDurableQueueToBackendAndRestart(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var onBattery atomic.Bool
	var writes atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					switch scanner.Text() {
					case "LIST UPS":
						fmt.Fprint(conn, "BEGIN LIST UPS\nUPS ups_office \"Office\"\nEND LIST UPS\n")
					case `LIST VAR "ups_office"`:
						status := "OL"
						charge := "100"
						if onBattery.Load() {
							status = "OB LB"
							charge = "9"
						}
						fmt.Fprintf(conn, "BEGIN LIST VAR ups_office\nVAR ups_office ups.status \"%s\"\nVAR ups_office battery.charge \"%s\"\nVAR ups_office ups.load \"5\"\nVAR ups_office ups.realpower.nominal \"810\"\nEND LIST VAR ups_office\n", status, charge)
					default:
						writes.Add(1)
					}
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	p, _ := strconv.Atoi(port)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("integration-token"), 0600)
	var available atomic.Bool
	var received uplink.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !available.Load() {
			w.WriteHeader(503)
			return
		}
		if json.NewDecoder(r.Body).Decode(&received) != nil {
			t.Error("invalid delivery")
		}
		var ids []string
		for _, e := range received.Messages {
			ids = append(ids, e.ID)
		}
		json.NewEncoder(w).Encode(uplink.Receipt{AcknowledgedIDs: ids})
	}))
	defer backend.Close()
	c := config.Config{StateDir: dir, PollSeconds: 3, NUTTimeoutSeconds: 2, FailureThreshold: 2, LowBatteryPercent: 10, VoltageDropPercent: 15, MaxPending: 100, Sources: []config.Source{{ID: "pi", Host: host, Port: p}}, Backend: config.Backend{URL: backend.URL, TokenFile: token, AllowHTTPLoopback: true}}
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := Runner{Config: c, Store: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err = r.Run(context.Background(), true); err == nil {
		t.Fatal("503 should leave delivery pending")
	}
	d, _ := state.Read(dir)
	if len(d.Pending) != 1 || len(d.Devices) != 1 {
		t.Fatal("reading not persisted before upload")
	}
	firstID := d.Pending[0].ID
	onBattery.Store(true)
	if err = r.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r.Store = s
	available.Store(true)
	if err = r.sendOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, _ = state.Read(dir)
	if len(d.Pending) != 0 || len(received.Messages) != 2 || received.Messages[0].ID != firstID {
		t.Fatal("backlog not delivered")
	}
	var kinds []string
	for _, e := range received.Messages {
		for _, event := range e.Events {
			kinds = append(kinds, event.Kind)
		}
	}
	if strings.Join(kinds, ",") != "on_battery,low_battery" || writes.Load() != 0 {
		t.Fatal("unexpected events or hardware write", kinds, writes.Load())
	}
	// Backend failure must not block the independent polling loop.
	available.Store(false)
	r.Config.PollSeconds = 1
	ctxLimited, stopLimited := context.WithTimeout(context.Background(), 2300*time.Millisecond)
	if err = r.Run(ctxLimited, false); err != context.DeadlineExceeded {
		t.Fatal("unexpected continuous-run shutdown", err)
	}
	stopLimited()
	d, err = state.Read(dir)
	if err != nil || d.Sequence < 5 || len(d.Pending) < 3 {
		t.Fatal("backend failure blocked NUT polling", err, d.Sequence, len(d.Pending))
	}
	// Stop an idle uploader without waiting for its retry window or leaving polling goroutines behind.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err = r.Run(ctx, false); err != context.Canceled || time.Since(start) > time.Second {
		t.Fatal("shutdown did not cancel cleanly", err)
	}
}
