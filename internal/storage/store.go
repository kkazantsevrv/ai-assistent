// Package storage хранит напоминания, заметки и настройки в JSON-файле.
// Для личного бота этого более чем достаточно, а главное — нет CGO и
// внешних баз, поэтому всё одинаково работает на ПК и в Termux.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu   sync.Mutex
	path string
	data *Data
}

// Open открывает (или создаёт) хранилище в каталоге dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "store.json")}

	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.data = &Data{}
	case err != nil:
		return nil, err
	default:
		var d Data
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("повреждён %s: %w (есть резервная копия store.json.bak?)", s.path, err)
		}
		s.data = &d
		// Резервная копия последнего успешно прочитанного состояния.
		_ = os.WriteFile(s.path+".bak", raw, 0o600)
	}
	if s.data.Chats == nil {
		s.data.Chats = map[int64]*ChatState{}
	}
	s.data.Version = 1
	return s, nil
}

// Read выполняет fn под блокировкой, без сохранения.
func (s *Store) Read(fn func(d *Data)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.data)
}

// Write выполняет fn под блокировкой и сохраняет данные на диск.
func (s *Store) Write(fn func(d *Data)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.data)
	return s.save()
}

// WriteIf сохраняет данные, только если fn вернула true.
func (s *Store) WriteIf(fn func(d *Data) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !fn(s.data) {
		return nil
	}
	return s.save()
}

func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.data, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ---------- помощники над Data (вызывать внутри Read/Write) ----------

// Chat возвращает состояние чата, создавая его с настройками по умолчанию.
func (d *Data) Chat(chatID int64, defaults Settings, now time.Time) *ChatState {
	c, ok := d.Chats[chatID]
	if !ok {
		c = &ChatState{Settings: defaults}
		// Не присылать сводку в день знакомства, если её время уже прошло.
		c.LastDigest = now.Format("2006-01-02")
		d.Chats[chatID] = c
	}
	return c
}

func (d *Data) AddReminder(r *Reminder) *Reminder {
	d.NextReminderID++
	r.ID = d.NextReminderID
	d.Reminders = append(d.Reminders, r)
	return r
}

func (d *Data) Reminder(chatID, id int64) *Reminder {
	for _, r := range d.Reminders {
		if r.ID == id && r.ChatID == chatID {
			return r
		}
	}
	return nil
}

// ActiveReminders — активные напоминания чата, отсортированные по времени.
func (d *Data) ActiveReminders(chatID int64) []*Reminder {
	var out []*Reminder
	for _, r := range d.Reminders {
		if r.ChatID == chatID && !r.Done {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func (d *Data) DeleteReminder(chatID, id int64) bool {
	for i, r := range d.Reminders {
		if r.ID == id && r.ChatID == chatID {
			d.Reminders = append(d.Reminders[:i], d.Reminders[i+1:]...)
			return true
		}
	}
	return false
}

// DeleteGroup удаляет все напоминания группы (без учёта регистра), возвращает число удалённых.
func (d *Data) DeleteGroup(chatID int64, group string) int {
	n := 0
	kept := d.Reminders[:0]
	for _, r := range d.Reminders {
		if r.ChatID == chatID && group != "" && strings.EqualFold(r.Group, group) {
			n++
			continue
		}
		kept = append(kept, r)
	}
	d.Reminders = kept
	return n
}

// CleanupDone удаляет выполненные напоминания старше olderThan.
func (d *Data) CleanupDone(now time.Time, olderThan time.Duration) bool {
	changed := false
	kept := d.Reminders[:0]
	for _, r := range d.Reminders {
		if r.Done && r.DoneAt != nil && now.Sub(*r.DoneAt) > olderThan {
			changed = true
			continue
		}
		kept = append(kept, r)
	}
	d.Reminders = kept
	return changed
}

func (d *Data) AddNote(n *Note) *Note {
	d.NextNoteID++
	n.ID = d.NextNoteID
	d.Notes = append(d.Notes, n)
	return n
}

func (d *Data) Note(chatID, id int64) *Note {
	for _, n := range d.Notes {
		if n.ID == id && n.ChatID == chatID {
			return n
		}
	}
	return nil
}

func (d *Data) ChatNotes(chatID int64) []*Note {
	var out []*Note
	for _, n := range d.Notes {
		if n.ChatID == chatID {
			out = append(out, n)
		}
	}
	return out
}

func (d *Data) DeleteNote(chatID, id int64) bool {
	for i, n := range d.Notes {
		if n.ID == id && n.ChatID == chatID {
			d.Notes = append(d.Notes[:i], d.Notes[i+1:]...)
			return true
		}
	}
	return false
}
