package nut

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"omstudioit.com/nut-ups-agent/internal/config"
)

// Codes are deliberately fixed strings: never propagate a server response or command containing a password.
type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }
func code(s string) error      { return &Error{s} }
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "unavailable"
}

type Result struct {
	Source     config.Source
	Reachable  bool
	ErrorCode  string
	Devices    []string
	Variables  map[string]map[string]string
	FailedUPS  []string
	ObservedAt time.Time
}

type Client struct{ Timeout time.Duration }
type session struct {
	conn      net.Conn
	reader    *bufio.Scanner
	remaining int
}

func (c Client) Poll(ctx context.Context, source config.Source) Result {
	r := Result{Source: source, Variables: map[string]map[string]string{}}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", source.Address())
	if err != nil {
		r.ErrorCode = "unavailable"
		return r
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		r.ErrorCode = "unavailable"
		return r
	}
	scan := bufio.NewScanner(conn)
	scan.Buffer(make([]byte, 4096), 16384)
	s := session{conn: conn, reader: scan, remaining: 2 * 1024 * 1024}
	if source.Username != "" {
		password, e := config.ReadSecret(source.PasswordFile)
		if e != nil {
			r.ErrorCode = "credentials_unavailable"
			return r
		}
		for _, cmd := range []string{"USERNAME " + quote(source.Username), "PASSWORD " + quote(password)} {
			if e = s.write(cmd); e == nil {
				var row []string
				row, e = s.line()
				if e == nil && !equal(row, []string{"OK"}) {
					e = code("authentication_failed")
				}
			}
			if e != nil {
				r.ErrorCode = "authentication_failed"
				if Code(e) == "unavailable" {
					r.ErrorCode = "unavailable"
				}
				return r
			}
		}
	}
	rows, err := s.list([]string{"UPS"})
	if err != nil {
		r.ErrorCode = Code(err)
		return r
	}
	r.Reachable = true
	r.Devices = []string{}
	discovered := map[string]bool{}
	for _, row := range rows {
		if len(row) != 3 || row[0] != "UPS" || !validKey(row[1]) || discovered[row[1]] || len(r.Devices) >= 64 {
			r.ErrorCode = "invalid_response"
			r.Devices = nil
			return r
		}
		discovered[row[1]] = true
		r.Devices = append(r.Devices, row[1])
	}
	keys := r.Devices
	if len(source.UPSKeys) > 0 {
		keys = source.UPSKeys
	}
	for _, key := range keys {
		if !discovered[key] {
			r.FailedUPS = append(r.FailedUPS, key)
			continue
		}
		rows, e := s.list([]string{"VAR", key})
		if e != nil {
			r.FailedUPS = append(r.FailedUPS, key)
			continue
		}
		vars := map[string]string{}
		valid := true
		for _, row := range rows {
			if len(row) != 4 || row[0] != "VAR" || row[1] != key || row[2] == "" {
				valid = false
				break
			}
			if _, ok := vars[row[2]]; ok {
				valid = false
				break
			}
			vars[row[2]] = row[3]
		}
		if !valid || len(vars) == 0 {
			r.FailedUPS = append(r.FailedUPS, key)
		} else {
			r.Variables[key] = vars
		}
	}
	r.ObservedAt = time.Now().UTC()
	return r
}

func validKey(s string) bool { return s != "" && len(s) <= 200 && !strings.ContainsAny(s, "\r\n\x00") }
func quote(s string) string  { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
func (s *session) write(command string) error {
	if _, err := io.WriteString(s.conn, command+"\n"); err != nil {
		return code("unavailable")
	}
	return nil
}
func (s *session) line() ([]string, error) {
	if !s.reader.Scan() {
		if errors.Is(s.reader.Err(), bufio.ErrTooLong) {
			return nil, code("response_too_large")
		}
		return nil, code("unavailable")
	}
	raw := s.reader.Text()
	s.remaining -= len(raw) + 1
	if s.remaining < 0 {
		return nil, code("response_too_large")
	}
	t, err := tokens(raw)
	if err != nil {
		return nil, err
	}
	if len(t) > 0 && t[0] == "ERR" {
		if len(t) > 1 && (t[1] == "ACCESS-DENIED" || t[1] == "PASSWORD-REQUIRED" || t[1] == "USERNAME-REQUIRED") {
			return nil, code("authentication_required")
		}
		return nil, code("read_error")
	}
	return t, nil
}
func (s *session) list(query []string) ([][]string, error) {
	args := make([]string, len(query))
	for i, v := range query {
		args[i] = quote(v)
	}
	// NUT keywords remain bare, only device arguments are quoted.
	args[0] = query[0]
	if err := s.write("LIST " + strings.Join(args, " ")); err != nil {
		return nil, err
	}
	row, err := s.line()
	if err != nil {
		return nil, err
	}
	if !equal(row, append([]string{"BEGIN", "LIST"}, query...)) {
		return nil, code("invalid_response")
	}
	var rows [][]string
	for len(rows) <= 4096 {
		row, err = s.line()
		if err != nil {
			return nil, err
		}
		if equal(row, append([]string{"END", "LIST"}, query...)) {
			return rows, nil
		}
		rows = append(rows, row)
	}
	return nil, code("response_too_large")
}
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tokens(line string) ([]string, error) {
	var out []string
	var b strings.Builder
	quoted, escaped, started := false, false, false
	flush := func() {
		if started {
			out = append(out, b.String())
			b.Reset()
			started = false
		}
	}
	for _, ch := range line {
		if ch == 0 {
			return nil, code("invalid_response")
		}
		if escaped {
			b.WriteRune(ch)
			started = true
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			started = true
			continue
		}
		if quoted {
			if ch == '"' {
				quoted = false
				flush()
			} else {
				b.WriteRune(ch)
			}
			continue
		}
		switch ch {
		case '#':
			flush()
			return out, nil
		case ' ', '\t', '\r':
			flush()
		case '=':
			flush()
			out = append(out, "=")
		case '"':
			if !started {
				quoted = true
				started = true
			} else {
				b.WriteRune(ch)
			}
		default:
			b.WriteRune(ch)
			started = true
		}
	}
	if quoted || escaped {
		return nil, code("invalid_response")
	}
	flush()
	return out, nil
}
