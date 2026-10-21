package state

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"omstudioit.com/nut-ups-agent/internal/config"
	"omstudioit.com/nut-ups-agent/internal/model"
	"omstudioit.com/nut-ups-agent/internal/nut"
)

const maxStateBytes = 64 * 1024 * 1024
const maxQueueBytes = 16 * 1024 * 1024

type Data struct {
	Version         int               `json:"version"`
	Revision        uint64            `json:"revision"`
	AgentID         string            `json:"agent_id"`
	Sequence        uint64            `json:"sequence"`
	Sources         map[string]Source `json:"sources"`
	Devices         map[string]Device `json:"devices"`
	RecentEvents    []model.Event     `json:"recent_events"`
	Pending         []model.Envelope  `json:"pending"`
	DroppedMessages uint64            `json:"dropped_messages_total"`
}

type Store struct {
	mu       sync.Mutex
	dir      string
	lock     *os.File
	journal  *os.File
	data     Data
	poisoned bool
}

func readCheckpoint(dir string) (Data, error) {
	var data Data
	f, err := os.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		return data, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > maxStateBytes {
		return data, errors.New("saved agent state exceeds its size limit")
	}
	d := json.NewDecoder(io.LimitReader(f, maxStateBytes+1))
	if err = d.Decode(&data); err != nil {
		return data, errors.New("agent state is damaged; preserve it and restore a backup before restarting")
	}
	if d.Decode(new(any)) != io.EOF || data.Version != 1 || data.AgentID == "" || data.Sources == nil || data.Devices == nil {
		return data, errors.New("unsupported or damaged agent state")
	}
	return data, nil
}

// A status reader can race a checkpoint rotation. Retry against the newer checkpoint.
func Read(dir string) (Data, error) {
	for attempt := 0; attempt < 3; attempt++ {
		d, err := readCheckpoint(dir)
		if err != nil {
			return d, err
		}
		revision := d.Revision
		_, replayErr := replay(dir, &d)
		latest, err := readCheckpoint(dir)
		if err != nil {
			return d, err
		}
		if latest.Revision != revision {
			continue
		}
		return d, replayErr
	}
	return Data{}, errors.New("agent checkpoint is changing; retry status")
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("cannot create agent state directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0027 != 0 {
		return nil, errors.New("state directory must be private (0700 or 0750) and not a symlink")
	}
	lock, err := acquireLock(filepath.Join(dir, "agent.lock"))
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, lock: lock}
	s.data, err = readCheckpoint(dir)
	if os.IsNotExist(err) {
		if _, journalErr := os.Stat(filepath.Join(dir, "journal.bin")); !os.IsNotExist(journalErr) {
			s.Close()
			return nil, errors.New("agent checkpoint is missing; preserve the existing journal for recovery")
		}
		s.data = Data{Version: 1, AgentID: model.NewID(), Sources: map[string]Source{}, Devices: map[string]Device{}}
		err = s.save(s.data)
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	if _, journalErr := os.Stat(filepath.Join(dir, "journal.bin")); os.IsNotExist(journalErr) && s.data.Revision > 0 {
		s.Close()
		return nil, errors.New("agent journal is missing; restore the checkpoint and journal together")
	}
	end, err := replay(dir, &s.data)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.journal, err = os.OpenFile(filepath.Join(dir, "journal.bin"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err == nil {
		err = s.journal.Truncate(end)
	}
	if err == nil {
		err = s.journal.Sync()
	}
	if err == nil {
		var directory *os.File
		directory, err = os.Open(dir)
		if err == nil {
			err = directory.Sync()
			directory.Close()
		}
	}
	if err != nil {
		s.Close()
		return nil, errors.New("cannot initialize durable agent journal")
	}
	return s, nil
}
func (s *Store) Close() {
	if s.journal != nil {
		s.journal.Close()
		s.journal = nil
	}
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
}

// Atomic checkpoints plus a synchronized transaction journal commit readings,
// event detector and queue together, without rewriting the backlog on every poll.
func (s *Store) save(data Data) error {
	b, err := json.Marshal(data)
	if err != nil || len(b) > maxStateBytes {
		return errors.New("agent state exceeds its size limit")
	}
	f, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return errors.New("cannot create state transaction")
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("cannot persist agent state")
	}
	if err = os.Rename(name, filepath.Join(s.dir, "state.json")); err != nil {
		return errors.New("cannot commit agent state")
	}
	dir, err := os.Open(s.dir)
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		s.poisoned = true
		return errors.New("cannot sync agent state directory; restart required")
	}
	return nil
}
func (s *Store) transaction(fn func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return errors.New("state persistence unavailable; restart required")
	}
	b, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	var next Data
	if err = json.Unmarshal(b, &next); err != nil {
		return err
	}
	if err = fn(&next); err != nil {
		return err
	}
	next.Revision = s.data.Revision + 1
	if err = s.appendTransaction(s.data, next); err != nil {
		return err
	}
	s.data = next
	info, err := s.journal.Stat()
	if err != nil {
		s.poisoned = true
		return errors.New("cannot inspect durable agent journal")
	}
	if info.Size() >= 4*1024*1024 {
		if err = s.save(next); err == nil {
			err = s.journal.Truncate(0)
		}
		if err == nil {
			err = s.journal.Sync()
		}
		if err != nil {
			s.poisoned = true
			return errors.New("cannot rotate agent journal; restart required")
		}
	}
	return nil
}

func (s *Store) Record(results []nut.Result, c config.Config, at time.Time) error {
	at = at.UTC()
	return s.transaction(func(d *Data) error {
		d.Sequence++
		e := model.Envelope{SchemaVersion: 1, AgentID: d.AgentID, ID: model.NewID(), Sequence: d.Sequence, RecordedAt: at}
		active := map[string]bool{}
		for _, source := range c.Sources {
			active[model.SourceID(source)] = true
		}
		for id := range d.Sources {
			if !active[id] {
				delete(d.Sources, id)
			}
		}
		for id, v := range d.Devices {
			if !active[v.Reading.SourceID] {
				delete(d.Devices, id)
			}
		}
		for _, result := range results {
			id := model.SourceID(result.Source)
			old := d.Sources[id]
			old.ID = id
			old.Name = result.Source.Name
			old.LastAttempt = at
			old.ErrorCode = result.ErrorCode
			o := model.SourceObservation{SourceID: id, Name: result.Source.Name, Connection: "reachable", ErrorCode: result.ErrorCode, DiscoveredUPS: result.Devices, UnavailableUPS: result.FailedUPS}
			if result.ErrorCode != "" {
				if result.ErrorCode == "unavailable" {
					o.Connection = "unavailable"
					old.Failures++
					if old.FirstFailureAt == nil {
						t := at
						old.FirstFailureAt = &t
					}
					if old.Failures >= c.FailureThreshold && old.DisconnectedAt == nil {
						old.DisconnectedAt = old.FirstFailureAt
						e.Events = append(e.Events, event("nut_server_unavailable", id, "", *old.DisconnectedAt))
					}
				} else {
					o.Connection = "read_error"
					old.Failures = 0
					old.FirstFailureAt = nil
				}
				for key, device := range d.Devices {
					if device.Reading.SourceID == id {
						device.Stale = true
						d.Devices[key] = device
					}
				}
			} else {
				if old.DisconnectedAt != nil {
					ev := event("nut_server_reconnected", id, "", at)
					seconds := at.Sub(*old.DisconnectedAt).Seconds()
					if seconds < 0 {
						seconds = 0
					}
					ev.DisconnectedSeconds = &seconds
					e.Events = append(e.Events, ev)
				}
				old.Failures = 0
				old.DisconnectedAt = nil
				old.FirstFailureAt = nil
				t := at
				old.LastSuccess = &t
				// A successful discovery freezes readings for devices that stopped being advertised.
				for key, device := range d.Devices {
					if device.Reading.SourceID == id {
						device.Stale = true
						d.Devices[key] = device
					}
				}
				keys := make([]string, 0, len(result.Variables))
				for key := range result.Variables {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					observedAt := result.ObservedAt
					if observedAt.IsZero() {
						observedAt = at
					}
					reading := model.NewReading(id, key, result.Variables[key], observedAt)
					previous := d.Devices[reading.DeviceID]
					// Staleness from this poll must not make every normal transition uncertain.
					if original, ok := s.data.Devices[reading.DeviceID]; ok {
						previous.Stale = original.Stale
					}
					device, events := observe(previous, reading, c)
					d.Devices[reading.DeviceID] = device
					e.Readings = append(e.Readings, reading)
					e.Events = append(e.Events, events...)
				}
			}
			d.Sources[id] = old
			e.Sources = append(e.Sources, o)
		}
		// A rotating server inventory cannot grow the last-known cache without bound.
		if len(d.Devices) > 1024 {
			var stale []string
			for id, v := range d.Devices {
				if v.Stale {
					stale = append(stale, id)
				}
			}
			sort.Slice(stale, func(i, j int) bool {
				return d.Devices[stale[i]].Reading.ObservedAt.Before(d.Devices[stale[j]].Reading.ObservedAt)
			})
			for _, id := range stale {
				if len(d.Devices) <= 1024 {
					break
				}
				delete(d.Devices, id)
			}
		}
		staleIDs := make([]string, 0)
		for id, device := range d.Devices {
			if device.Stale {
				staleIDs = append(staleIDs, id)
			}
		}
		sort.Strings(staleIDs)
		for _, id := range staleIDs {
			e.LastKnown = append(e.LastKnown, d.Devices[id].Reading)
		}
		d.RecentEvents = append(d.RecentEvents, e.Events...)
		if len(d.RecentEvents) > 256 {
			d.RecentEvents = d.RecentEvents[len(d.RecentEvents)-256:]
		}
		d.Pending = append(d.Pending, e)
		sizes := make([]int, len(d.Pending))
		total := 0
		for i, v := range d.Pending {
			b, _ := json.Marshal(v)
			sizes[i] = len(b)
			total += len(b)
		}
		for len(d.Pending) > c.MaxPending || total > maxQueueBytes {
			// Prefer dropping telemetry without events. Event loss is still explicitly counted if every queued message contains events.
			drop := 0
			for i, v := range d.Pending {
				if len(v.Events) == 0 {
					drop = i
					break
				}
			}
			total -= sizes[drop]
			d.Pending = append(d.Pending[:drop], d.Pending[drop+1:]...)
			sizes = append(sizes[:drop], sizes[drop+1:]...)
			d.DroppedMessages++
		}
		if len(d.Pending) > 0 && d.Pending[len(d.Pending)-1].ID == e.ID {
			d.Pending[len(d.Pending)-1].DroppedMessages = d.DroppedMessages
		}
		return nil
	})
}

func (s *Store) Batch() []model.Envelope {
	return s.BatchThrough(0)
}

// Freeze each upload cycle at a sequence so new ordinary polls do not turn a
// slow backlog drain into continuous uploads. Events wake the next cycle.
func (s *Store) BatchThrough(sequence uint64) []model.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Envelope
	bytes := 0
	for _, e := range s.data.Pending {
		if sequence != 0 && e.Sequence > sequence {
			break
		}
		b, _ := json.Marshal(e)
		if len(out) > 0 && (len(out) >= 100 || bytes+len(b) > 512*1024) {
			break
		}
		bytes += len(b)
		out = append(out, e)
	}
	// Return a detached snapshot; simultaneous polling never changes an in-flight request.
	b, _ := json.Marshal(out)
	var copy []model.Envelope
	_ = json.Unmarshal(b, &copy)
	return copy
}

func (s *Store) UploadState() (sequence uint64, urgent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.data.Pending {
		sequence = message.Sequence
		urgent = urgent || len(message.Events) > 0
	}
	return
}
func (s *Store) Acknowledge(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return s.transaction(func(d *Data) error {
		keep := d.Pending[:0]
		for _, e := range d.Pending {
			if !set[e.ID] {
				keep = append(keep, e)
			}
		}
		d.Pending = keep
		return nil
	})
}
func (s *Store) Stats() (int, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data.Pending), s.data.DroppedMessages
}
func (s *Store) AgentID() string { s.mu.Lock(); defer s.mu.Unlock(); return s.data.AgentID }
func (d Data) Summary() any {
	return struct {
		AgentID  string            `json:"agent_id"`
		Sequence uint64            `json:"sequence"`
		Pending  int               `json:"pending_messages"`
		Dropped  uint64            `json:"dropped_messages_total"`
		Sources  map[string]Source `json:"sources"`
		Devices  map[string]Device `json:"devices"`
		Events   []model.Event     `json:"recent_events"`
	}{d.AgentID, d.Sequence, len(d.Pending), d.DroppedMessages, d.Sources, d.Devices, d.RecentEvents}
}
