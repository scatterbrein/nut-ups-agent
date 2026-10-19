package state

import (
	"encoding/binary"
	"fmt"
	"omstudioit.com/nut-ups-agent/internal/nut"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIncompleteTransactionRecoveryAndCheckpointReplay(t *testing.T) {
	dir := t.TempDir()
	c := settings(dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	record(t, s, result(c, "OL", "100", "125"), c, time.Now())
	first := s.Batch()[0].ID
	// Simulate a crash after checkpoint rename but before journal truncation.
	if err = s.save(s.data); err != nil {
		t.Fatal(err)
	}
	s.Close()
	path := filepath.Join(dir, "journal.bin")
	info, _ := os.Stat(path)
	validSize := info.Size()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	var partial [12]byte
	binary.BigEndian.PutUint32(partial[:4], 100)
	copy(partial[8:], "{cut")
	f.Write(partial[:])
	f.Sync()
	f.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if info.Size() != validSize || len(s.Batch()) != 1 || s.Batch()[0].ID != first {
		t.Fatal("partial transaction recovery lost or duplicated a committed record")
	}
	record(t, s, result(c, "OB", "87", "0"), c, time.Now().Add(3*time.Second))
	s.Close()
	d, err := Read(dir)
	if err != nil || len(d.Pending) != 2 || count(d, "on_battery") != 1 {
		t.Fatal("recovered journal cannot accept new transactions", err)
	}
}
func TestCorruptJournalAndMissingCheckpointArePreserved(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			dir := t.TempDir()
			c := settings(dir)
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			record(t, s, result(c, "OL", "100", "125"), c, time.Now())
			s.Close()
			path := filepath.Join(dir, "journal.bin")
			b, _ := os.ReadFile(path)
			if missing {
				os.Remove(filepath.Join(dir, "state.json"))
			} else {
				b[len(b)-1] ^= 1
				os.WriteFile(path, b, 0600)
			}
			if s, err = Open(dir); err == nil {
				s.Close()
				t.Fatal("corrupt/missing data was silently reset")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(b) {
				t.Fatal("evidence overwritten")
			}
		})
	}
}
func TestJournalCompactionKeepsQueueAndLatestState(t *testing.T) {
	dir := t.TempDir()
	c := settings(dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := nut.Result{Source: c.Sources[0], Reachable: true, Variables: map[string]map[string]string{}}
	for i := 0; i < 64; i++ {
		key := fmt.Sprintf("ups_%02d", i)
		r.Devices = append(r.Devices, key)
		r.Variables[key] = map[string]string{"ups.status": "OL", "battery.charge": "100", "device.mfr": strings.Repeat("a", 256), "device.model": strings.Repeat("b", 256), "ups.mfr": strings.Repeat("c", 256), "ups.model": strings.Repeat("d", 256)}
	}
	at := time.Now()
	for i := 0; i < 35; i++ {
		record(t, s, r, c, at.Add(time.Duration(i)*3*time.Second))
	}
	checkpoint, err := readCheckpoint(dir)
	if err != nil || checkpoint.Revision == 0 {
		t.Fatal("journal did not compact", err)
	}
	d, err := Read(dir)
	if err != nil || d.Sequence != 35 || len(d.Pending) != 10 || len(d.Devices) != 64 {
		t.Fatal("compaction changed state", err)
	}
	s.Close()
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if pending, _ := reopened.Stats(); pending != 10 {
		t.Fatal("compacted queue not recovered")
	}
}
