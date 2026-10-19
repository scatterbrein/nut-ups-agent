package uplink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
)

type Request struct {
	SchemaVersion int              `json:"schema_version"`
	AgentID       string           `json:"agent_id"`
	Messages      []model.Envelope `json:"messages"`
}
type Receipt struct {
	AcknowledgedIDs []string `json:"acknowledged_ids"`
}
type RetryError struct {
	Status int
	After  time.Duration
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("backend rejected upload (HTTP %d)", e.Status)
}

type Sender struct {
	Backend config.Backend
	client  *http.Client
}

func New(backend config.Backend) *Sender {
	return &Sender{Backend: backend, client: &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second},
		// Never forward the bearer token to a redirect destination, including another path on the same host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *Sender) Send(ctx context.Context, messages []model.Envelope) ([]string, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	secret, err := config.ReadSecret(s.Backend.TokenFile)
	if err != nil {
		return nil, errors.New("backend token unavailable")
	}
	agent := messages[0].AgentID
	ids := map[string]bool{}
	for _, m := range messages {
		if m.AgentID != agent || m.ID == "" || ids[m.ID] {
			return nil, errors.New("invalid local upload batch")
		}
		ids[m.ID] = true
	}
	b, err := json.Marshal(Request{1, agent, messages})
	if err != nil || len(b) > 8*1024*1024 {
		return nil, errors.New("upload batch exceeds its size limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Backend.URL, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("invalid backend URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("User-Agent", "NUT-UPS-Agent/0.1")
	parts := []string{agent}
	for _, m := range messages {
		parts = append(parts, m.ID)
	}
	req.Header.Set("Idempotency-Key", model.Hash(parts...))
	response, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("backend could not be reached securely")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &RetryError{response.StatusCode, retryAfter(response.Header.Get("Retry-After"), time.Now())}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return nil, errors.New("backend receipt is unreadable or too large")
	}
	var receipt Receipt
	if json.Unmarshal(body, &receipt) != nil || len(receipt.AcknowledgedIDs) == 0 {
		return nil, errors.New("backend did not explicitly acknowledge any messages")
	}
	seen := map[string]bool{}
	for _, id := range receipt.AcknowledgedIDs {
		if !ids[id] || seen[id] {
			return nil, errors.New("backend receipt contains unexpected or repeated message IDs")
		}
		seen[id] = true
	}
	return receipt.AcknowledgedIDs, nil
}

func retryAfter(value string, now time.Time) time.Duration {
	var delay time.Duration
	if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
		if n > 86400 {
			n = 86400
		}
		delay = time.Duration(n) * time.Second
	} else if date, err := http.ParseTime(value); err == nil {
		delay = date.Sub(now)
	}
	if delay < 0 {
		delay = 0
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}

func Backoff(failures int, err error) time.Duration {
	if failures > 8 {
		failures = 8
	}
	if failures < 1 {
		failures = 1
	}
	d := time.Second * time.Duration(1<<uint(failures-1))
	d += time.Duration(rand.Int64N(int64(d/2) + 1))
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	var retry *RetryError
	if errors.As(err, &retry) {
		if retry.Status == 401 || retry.Status == 403 {
			if d < 5*time.Minute {
				d = 5 * time.Minute
			}
		}
		if retry.After > d {
			d = retry.After
		}
	}
	return d
}
