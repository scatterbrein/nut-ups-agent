package nut

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"omstudioit.com/nut-ups-agent/internal/config"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fakeServer(t *testing.T, serve func(net.Conn)) config.Source {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			serve(conn)
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return config.Source{ID: "test", Host: host, Port: p}
}
func TestAnonymousDiscoveryAndEscapedUPS(t *testing.T) {
	commands := make(chan []string, 1)
	s := fakeServer(t, func(conn net.Conn) {
		r := bufio.NewReader(conn)
		var seen []string
		line, _ := r.ReadString('\n')
		seen = append(seen, strings.TrimSpace(line))
		fmt.Fprint(conn, "BEGIN LIST UPS\nUPS \"ups #office\" \"Office UPS\"\nEND LIST UPS\n")
		line, _ = r.ReadString('\n')
		seen = append(seen, strings.TrimSpace(line))
		fmt.Fprint(conn, "BEGIN LIST VAR \"ups #office\"\nVAR \"ups #office\" ups.status \"OL\"\nVAR \"ups #office\" battery.charge \"100\"\nEND LIST VAR \"ups #office\"\n")
		commands <- seen
	})
	result := (Client{Timeout: time.Second}).Poll(context.Background(), s)
	if result.ErrorCode != "" || result.Variables["ups #office"]["ups.status"] != "OL" {
		t.Fatal(result)
	}
	seen := <-commands
	if len(seen) != 2 || seen[0] != "LIST UPS" || seen[1] != `LIST VAR "ups #office"` {
		t.Fatal("unexpected commands", seen)
	}
}
func TestAuthenticationFailureDoesNotLeakPassword(t *testing.T) {
	commands := make(chan []string, 1)
	s := fakeServer(t, func(conn net.Conn) {
		r := bufio.NewReader(conn)
		var seen []string
		line, _ := r.ReadString('\n')
		seen = append(seen, strings.TrimSpace(line))
		fmt.Fprint(conn, "OK\n")
		line, _ = r.ReadString('\n')
		seen = append(seen, strings.TrimSpace(line))
		fmt.Fprint(conn, "ERR ACCESS-DENIED secret-value\n")
		commands <- seen
	})
	s.Username = "upsapp"
	s.PasswordFile = filepath.Join(t.TempDir(), "password")
	os.WriteFile(s.PasswordFile, []byte("secret-value"), 0600)
	r := (Client{Timeout: time.Second}).Poll(context.Background(), s)
	if r.ErrorCode != "authentication_failed" {
		t.Fatal(r.ErrorCode)
	}
	seen := <-commands
	if len(seen) != 2 || !strings.HasPrefix(seen[1], "PASSWORD ") {
		t.Fatal(seen)
	}
}
func TestCrossDeviceAndDuplicateVariablesAreRejected(t *testing.T) {
	for name, body := range map[string]string{"wrong UPS": "VAR wrong ups.status \"OL\"", "duplicate": "VAR ups ups.status \"OL\"\nVAR ups ups.status \"OB\""} {
		t.Run(name, func(t *testing.T) {
			s := fakeServer(t, func(conn net.Conn) {
				r := bufio.NewReader(conn)
				r.ReadString('\n')
				fmt.Fprint(conn, "BEGIN LIST UPS\nUPS ups \"UPS\"\nEND LIST UPS\n")
				r.ReadString('\n')
				fmt.Fprintf(conn, "BEGIN LIST VAR ups\n%s\nEND LIST VAR ups\n", body)
			})
			result := (Client{Timeout: time.Second}).Poll(context.Background(), s)
			if len(result.Variables) != 0 || len(result.FailedUPS) != 1 {
				t.Fatal(result)
			}
		})
	}
}
func TestCancellationClosesConnection(t *testing.T) {
	done := make(chan struct{})
	s := fakeServer(t, func(conn net.Conn) {
		bufio.NewReader(conn).ReadString('\n')
		defer close(done)
		var b [1]byte
		conn.Read(b[:])
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	start := time.Now()
	r := (Client{Timeout: 5 * time.Second}).Poll(ctx, s)
	if time.Since(start) > time.Second || r.ErrorCode != "unavailable" {
		t.Fatal("cancellation ignored", r)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("socket left open")
	}
}
func TestParserNUTSyntax(t *testing.T) {
	got, err := tokens(`VAR "ups #1" device.model "A \"quoted\" \\ model" # comment`)
	if err != nil || len(got) != 4 || got[1] != "ups #1" || got[3] != `A "quoted" \ model` {
		t.Fatal(got, err)
	}
	for _, line := range []string{`"unfinished`, `dangling\`, "a\x00b"} {
		if _, err = tokens(line); err == nil {
			t.Fatal("malformed line accepted")
		}
	}
}
