package state

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"omstudioit.com/nut-ups-agent/internal/model"
)

type delta struct {
	Revision uint64             `json:"revision"`
	Sequence uint64             `json:"sequence"`
	Dropped  uint64             `json:"dropped"`
	Sources  map[string]*Source `json:"sources,omitempty"`
	Devices  map[string]*Device `json:"devices,omitempty"`
	Events   *[]model.Event     `json:"events,omitempty"`
	Add      []model.Envelope   `json:"add,omitempty"`
	Remove   []string           `json:"remove,omitempty"`
}

func difference(before, after Data) (delta, error) {
	d := delta{Revision: after.Revision, Sequence: after.Sequence, Dropped: after.DroppedMessages, Sources: map[string]*Source{}, Devices: map[string]*Device{}}
	for id, v := range after.Sources {
		if old, ok := before.Sources[id]; !ok || !reflect.DeepEqual(v, old) {
			copy := v
			d.Sources[id] = &copy
		}
	}
	for id := range before.Sources {
		if _, ok := after.Sources[id]; !ok {
			d.Sources[id] = nil
		}
	}
	for id, v := range after.Devices {
		if old, ok := before.Devices[id]; !ok || !reflect.DeepEqual(v, old) {
			copy := v
			d.Devices[id] = &copy
		}
	}
	for id := range before.Devices {
		if _, ok := after.Devices[id]; !ok {
			d.Devices[id] = nil
		}
	}
	if !reflect.DeepEqual(before.RecentEvents, after.RecentEvents) {
		d.Events = &after.RecentEvents
	}
	old := map[string]model.Envelope{}
	for _, e := range before.Pending {
		old[e.ID] = e
	}
	current := map[string]bool{}
	for _, e := range after.Pending {
		current[e.ID] = true
		if previous, ok := old[e.ID]; ok {
			oldJSON, _ := json.Marshal(previous)
			newJSON, _ := json.Marshal(e)
			if !bytes.Equal(oldJSON, newJSON) {
				return d, errors.New("pending telemetry must remain immutable")
			}
		} else {
			d.Add = append(d.Add, e)
		}
	}
	for _, e := range before.Pending {
		if !current[e.ID] {
			d.Remove = append(d.Remove, e.ID)
		}
	}
	return d, nil
}

func apply(d *Data, change delta) error {
	if change.Revision <= d.Revision {
		return nil
	} // Checkpoint committed before journal truncation.
	if change.Revision != d.Revision+1 {
		return errors.New("agent journal has a missing transaction")
	}
	for id, v := range change.Sources {
		if v == nil {
			delete(d.Sources, id)
		} else {
			d.Sources[id] = *v
		}
	}
	for id, v := range change.Devices {
		if v == nil {
			delete(d.Devices, id)
		} else {
			d.Devices[id] = *v
		}
	}
	if change.Events != nil {
		d.RecentEvents = *change.Events
	}
	remove := map[string]bool{}
	for _, id := range change.Remove {
		remove[id] = true
	}
	keep := d.Pending[:0]
	for _, e := range d.Pending {
		if !remove[e.ID] {
			keep = append(keep, e)
		}
	}
	d.Pending = append(keep, change.Add...)
	d.Revision = change.Revision
	d.Sequence = change.Sequence
	d.DroppedMessages = change.Dropped
	return nil
}

func (s *Store) appendTransaction(before, after Data) error {
	change, err := difference(before, after)
	if err != nil {
		return err
	}
	b, err := json.Marshal(change)
	if err != nil || len(b) > maxStateBytes {
		return errors.New("agent transaction exceeds its size limit")
	}
	frame := make([]byte, 8+len(b))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(b)))
	binary.BigEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(b))
	copy(frame[8:], b)
	n, err := s.journal.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = s.journal.Sync()
	}
	if err != nil {
		s.poisoned = true
		return errors.New("cannot sync agent transaction; restart required")
	}
	return nil
}

// Only an incomplete final frame is recoverable. A fully written frame with a bad
// checksum is corruption, never a reason to silently discard readings or events.
func replay(dir string, d *Data) (int64, error) {
	f, err := os.Open(filepath.Join(dir, "journal.bin"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, errors.New("cannot read agent journal")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 128*1024*1024 {
		return 0, errors.New("agent journal exceeds its size limit")
	}
	var offset int64
	for {
		var header [8]byte
		_, err = io.ReadFull(f, header[:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return offset, nil
		}
		if err != nil {
			return offset, errors.New("cannot read agent transaction header")
		}
		size := binary.BigEndian.Uint32(header[:4])
		if size == 0 || size > maxStateBytes {
			return offset, errors.New("damaged agent journal frame")
		}
		b := make([]byte, int(size))
		_, err = io.ReadFull(f, b)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return offset, nil
		}
		if err != nil {
			return offset, errors.New("cannot read agent transaction")
		}
		if crc32.ChecksumIEEE(b) != binary.BigEndian.Uint32(header[4:]) {
			return offset, errors.New("agent journal checksum failed; preserve files for recovery")
		}
		var change delta
		if json.Unmarshal(b, &change) != nil || change.Revision == 0 {
			return offset, errors.New("invalid agent journal transaction")
		}
		if err = apply(d, change); err != nil {
			return offset, err
		}
		offset += int64(8 + size)
	}
}
