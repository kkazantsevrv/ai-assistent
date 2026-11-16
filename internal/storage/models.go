package storage

import (
	"strings"
	"time"
)

// Типы повторения.
const (
	RepeatNone     = ""
	RepeatDaily    = "daily"
	RepeatWeekly   = "weekly"
	RepeatWeekdays = "weekdays"
	RepeatMonthly  = "monthly"
	RepeatYearly   = "yearly"
)

// NormalizeRepeat приводит произвольное значение к одной из констант.
func NormalizeRepeat(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none", "no", "once", "нет":
		return RepeatNone, true
	case "daily", "day", "ежедневно":
		return RepeatDaily, true
	case "weekly", "week", "еженедельно":
		return RepeatWeekly, true
	case "weekdays", "workdays", "будни":
		return RepeatWeekdays, true
	case "monthly", "month", "ежемесячно":
		return RepeatMonthly, true
	case "yearly", "annually", "year", "ежегодно":
		return RepeatYearly, true
	}
	return "", false
}

// Reminder — напоминание или событие расписания (в том числе повторяющееся).
type Reminder struct {
	ID     int64  `json:"id"`
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
	Notes  string `json:"notes,omitempty"`
	Group  string `json:"group,omitempty"` // название расписания, например «Пары»

	Start time.Time `json:"start"` // первое вхождение (якорь для повторов)
	At    time.Time `json:"at"`    // текущее/следующее вхождение

	// Offsets — за сколько минут до At присылать уведомления (0 — в момент события).
	Offsets []int `json:"offsets"`
	// Sent — какие из Offsets уже обработаны для текущего вхождения At.
	Sent []int `json:"sent,omitempty"`

	Repeat   string     `json:"repeat,omitempty"`
	Interval int        `json:"interval,omitempty"`
	Until    *time.Time `json:"until,omitempty"`

	Done      bool       `json:"done"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	Source    string     `json:"source,omitempty"`
}

// EffectiveOffsets возвращает Offsets или [0], если список пуст.
func (r *Reminder) EffectiveOffsets() []int {
	if len(r.Offsets) == 0 {
		return []int{0}
	}
	return r.Offsets
}

// Note — заметка.
type Note struct {
	ID        int64     `json:"id"`
	ChatID    int64     `json:"chat_id"`
	Text      string    `json:"text"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Settings — настройки конкретного чата.
type Settings struct {
	Timezone     string `json:"timezone"`
	RemindBefore int    `json:"remind_before"` // минут до события по умолчанию
	DigestTime   string `json:"digest_time"`   // "08:00" или "" — выключено
}

// ChatMessage — сообщение из истории диалога с ИИ.
type ChatMessage struct {
	Role    string    `json:"role"` // user | assistant
	Content string    `json:"content"`
	Time    time.Time `json:"time"`
}

// ChatState — состояние чата: настройки и история.
type ChatState struct {
	Settings   Settings      `json:"settings"`
	History    []ChatMessage `json:"history,omitempty"`
	LastDigest string        `json:"last_digest,omitempty"` // дата последней утренней сводки
}

// Data — всё, что хранится на диске.
type Data struct {
	Version        int                  `json:"version"`
	Owner          int64                `json:"owner,omitempty"`
	NextReminderID int64                `json:"next_reminder_id"`
	NextNoteID     int64                `json:"next_note_id"`
	Reminders      []*Reminder          `json:"reminders"`
	Notes          []*Note              `json:"notes"`
	Chats          map[int64]*ChatState `json:"chats"`
}
