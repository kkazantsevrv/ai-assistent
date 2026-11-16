// Package schedule — чистая логика повторений и срабатывания напоминаний.
// Здесь нет сети и хранилища, поэтому всё легко тестируется.
package schedule

import (
	"slices"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/storage"
)

// MissedGrace — сколько после события ещё имеет смысл прислать пропущенное
// уведомление (например, телефон был выключен или Termux «уснул»).
const MissedGrace = 12 * time.Hour

// Notification — уведомление, которое нужно отправить.
type Notification struct {
	Reminder storage.Reminder // копия на момент срабатывания
	EventAt  time.Time        // время вхождения, к которому относится уведомление
	Offset   int              // за сколько минут до события
	Missed   bool             // событие уже прошло — уведомление запоздало
}

// NextOccurrence возвращает первое вхождение повторяющегося напоминания строго
// после after. Вычисления ведутся в часовом поясе loc, поэтому «каждый день
// в 9:00» остаётся 9:00 и при переходе на летнее/зимнее время.
func NextOccurrence(r *storage.Reminder, after time.Time, loc *time.Location) (time.Time, bool) {
	if r.Repeat == storage.RepeatNone {
		return time.Time{}, false
	}
	iv := max(r.Interval, 1)
	start := r.Start
	if start.IsZero() {
		start = r.At
	}
	start = start.In(loc)

	var t time.Time
	switch r.Repeat {
	case storage.RepeatDaily, storage.RepeatWeekly:
		step := iv
		if r.Repeat == storage.RepeatWeekly {
			step = 7 * iv
		}
		k := 0
		if after.After(start) {
			k = max(int(after.Sub(start).Hours()/24)/step-1, 0)
		}
		for {
			c := start.AddDate(0, 0, k*step)
			if c.After(after) {
				t = c
				break
			}
			k++
		}

	case storage.RepeatWeekdays:
		c := start
		if after.After(start) {
			a := after.In(loc)
			c = time.Date(a.Year(), a.Month(), a.Day(), start.Hour(), start.Minute(), start.Second(), 0, loc)
		}
		for i := 0; i < 14; i++ {
			if c.After(after) && !c.Before(start) && isWeekday(c) {
				t = c
				break
			}
			c = c.AddDate(0, 0, 1)
		}
		if t.IsZero() {
			return time.Time{}, false
		}

	case storage.RepeatMonthly, storage.RepeatYearly:
		step := iv
		if r.Repeat == storage.RepeatYearly {
			step = 12 * iv
		}
		k := 0
		if after.After(start) {
			a := after.In(loc)
			months := (a.Year()-start.Year())*12 + int(a.Month()) - int(start.Month())
			k = max(months/step-1, 0)
		}
		for {
			c := AddMonthsClamped(start, k*step)
			if c.After(after) {
				t = c
				break
			}
			k++
		}

	default:
		return time.Time{}, false
	}

	if r.Until != nil && t.After(*r.Until) {
		return time.Time{}, false
	}
	return t, true
}

func isWeekday(t time.Time) bool {
	wd := t.Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

// AddMonthsClamped прибавляет n месяцев, не «перепрыгивая» в следующий месяц:
// 31 января + 1 месяц = 28/29 февраля.
func AddMonthsClamped(t time.Time, n int) time.Time {
	total := int(t.Month()) - 1 + n
	y := t.Year() + total/12
	m := total % 12
	if m < 0 {
		m += 12
		y--
	}
	month := time.Month(m + 1)
	day := min(t.Day(), daysIn(y, month))
	return time.Date(y, month, day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// AlignStart приводит время первого вхождения к допустимому значению
// (для «по будням» — на ближайший будний день) и, если оно в прошлом,
// переносит повторяющееся напоминание на ближайшее будущее вхождение.
// Возвращает false, если будущих вхождений нет.
func AlignStart(r *storage.Reminder, now time.Time, loc *time.Location) bool {
	if r.Repeat == storage.RepeatWeekdays {
		for !isWeekday(r.At.In(loc)) {
			r.At = r.At.In(loc).AddDate(0, 0, 1)
		}
	}
	r.Start = r.At
	if r.At.After(now) {
		return true
	}
	if r.Repeat == storage.RepeatNone {
		return false
	}
	next, ok := NextOccurrence(r, now, loc)
	if !ok {
		return false
	}
	r.At = next
	return true
}

// Occurrences возвращает вхождения напоминания в интервале [from, to).
func Occurrences(r *storage.Reminder, from, to time.Time, loc *time.Location, limit int) []time.Time {
	if r.Done {
		return nil
	}
	var out []time.Time
	t := r.At
	for guard := 0; guard < 5000 && t.Before(to) && len(out) < limit; guard++ {
		if !t.Before(from) {
			out = append(out, t)
		}
		next, ok := NextOccurrence(r, t, loc)
		if !ok {
			break
		}
		t = next
	}
	return out
}

// Process проверяет напоминание в момент now: отмечает наступившие
// уведомления, переводит повторяющиеся на следующее вхождение, а разовые —
// в выполненные. Возвращает уведомление для отправки (не больше одного) и
// признак того, что r изменилось.
func Process(r *storage.Reminder, now time.Time, loc *time.Location) (*Notification, bool) {
	var note *Notification
	changed := false

	for guard := 0; guard < 10000 && !r.Done; guard++ {
		var due []int
		pending := false
		for _, off := range r.EffectiveOffsets() {
			if slices.Contains(r.Sent, off) {
				continue
			}
			fire := r.At.Add(-time.Duration(off) * time.Minute)
			if now.Before(fire) {
				pending = true
			} else {
				due = append(due, off)
			}
		}

		if len(due) > 0 {
			// Если наступило сразу несколько (бот был выключен), шлём одно —
			// ближайшее к событию, чтобы не спамить.
			best := slices.Min(due)
			sendable := now.Before(r.At) || now.Sub(r.At) <= MissedGrace
			if sendable && note == nil {
				note = &Notification{
					Reminder: *r,
					EventAt:  r.At,
					Offset:   best,
					Missed:   now.Sub(r.At) > 2*time.Minute,
				}
			}
			r.Sent = append(r.Sent, due...)
			changed = true
		}

		// Ждём оставшиеся уведомления или само событие.
		if pending || now.Before(r.At) {
			return note, changed
		}

		// Вхождение полностью отработано.
		if next, ok := NextOccurrence(r, r.At, loc); ok {
			r.At = next
			r.Sent = nil
			changed = true
			if note != nil {
				// Следующее вхождение обработаем на следующем тике, чтобы
				// не потерять его собственное уведомление.
				return note, changed
			}
			continue
		}
		Complete(r, now)
		changed = true
	}
	return note, changed
}

// Complete помечает напоминание выполненным.
func Complete(r *storage.Reminder, now time.Time) {
	r.Done = true
	t := now
	r.DoneAt = &t
}

// CompleteOccurrence отмечает выполненным вхождение eventAt: разовое
// напоминание закрывается, повторяющееся переходит к следующему вхождению.
// Если вхождение уже в прошлом (At ушло вперёд), ничего не меняется.
func CompleteOccurrence(r *storage.Reminder, eventAt, now time.Time, loc *time.Location) bool {
	if r.Done {
		return false
	}
	if !eventAt.IsZero() && !r.At.Equal(eventAt) {
		return false
	}
	if next, ok := NextOccurrence(r, r.At, loc); ok {
		r.At = next
		r.Sent = nil
		return true
	}
	Complete(r, now)
	return true
}

// NormalizeOffsets убирает дубли и недопустимые значения, сортирует по убыванию.
func NormalizeOffsets(offs []int) []int {
	var out []int
	for _, o := range offs {
		if o < 0 || o > 60*24*60 || slices.Contains(out, o) {
			continue
		}
		out = append(out, o)
	}
	slices.Sort(out)
	slices.Reverse(out)
	if len(out) == 0 {
		return []int{0}
	}
	return out
}
