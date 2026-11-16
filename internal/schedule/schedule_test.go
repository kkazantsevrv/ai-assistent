package schedule

import (
	"testing"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/storage"
)

var msk = mustLoc("Europe/Moscow")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, msk)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNextOccurrence(t *testing.T) {
	cases := []struct {
		name     string
		repeat   string
		interval int
		start    string
		after    string
		want     string
	}{
		{"daily", storage.RepeatDaily, 1, "2026-11-01 09:00", "2026-11-16 10:00", "2026-11-17 09:00"},
		{"daily before", storage.RepeatDaily, 1, "2026-11-01 09:00", "2026-11-16 08:00", "2026-11-16 09:00"},
		{"every 2 days", storage.RepeatDaily, 2, "2026-11-01 09:00", "2026-11-02 10:00", "2026-11-03 09:00"},
		{"weekly", storage.RepeatWeekly, 1, "2026-11-02 18:30", "2026-11-16 19:00", "2026-11-23 18:30"},
		{"weekly exact", storage.RepeatWeekly, 1, "2026-11-02 18:30", "2026-11-16 18:30", "2026-11-23 18:30"},
		{"weekdays fri->mon", storage.RepeatWeekdays, 1, "2026-11-02 08:00", "2026-11-20 09:00", "2026-11-23 08:00"},
		{"monthly clamp", storage.RepeatMonthly, 1, "2026-01-31 12:00", "2026-02-01 00:00", "2026-02-28 12:00"},
		{"monthly back to 31", storage.RepeatMonthly, 1, "2026-01-31 12:00", "2026-02-28 13:00", "2026-03-31 12:00"},
		{"yearly", storage.RepeatYearly, 1, "2020-05-10 10:00", "2026-11-16 00:00", "2027-05-10 10:00"},
		{"after before start", storage.RepeatWeekly, 1, "2026-12-01 10:00", "2026-11-16 00:00", "2026-12-01 10:00"},
	}
	for _, c := range cases {
		r := &storage.Reminder{Repeat: c.repeat, Interval: c.interval, Start: at(c.start), At: at(c.start)}
		got, ok := NextOccurrence(r, at(c.after), msk)
		if !ok || !got.Equal(at(c.want)) {
			t.Errorf("%s: got %v ok=%v, want %s", c.name, got, ok, c.want)
		}
	}
}

func TestNextOccurrenceUntil(t *testing.T) {
	until := at("2026-11-20 23:59")
	r := &storage.Reminder{Repeat: storage.RepeatDaily, Start: at("2026-11-18 09:00"), At: at("2026-11-18 09:00"), Until: &until}
	if _, ok := NextOccurrence(r, at("2026-11-20 10:00"), msk); ok {
		t.Fatal("ожидалось отсутствие вхождений после until")
	}
}

func TestProcessOneTime(t *testing.T) {
	r := &storage.Reminder{ID: 1, Text: "x", At: at("2026-11-16 10:00"), Offsets: []int{15, 0}}
	r.Start = r.At

	if n, _ := Process(r, at("2026-11-16 09:40"), msk); n != nil {
		t.Fatal("рано для уведомления")
	}
	n, changed := Process(r, at("2026-11-16 09:45"), msk)
	if n == nil || n.Offset != 15 || !changed {
		t.Fatalf("ожидалось уведомление за 15 минут, получено %+v", n)
	}
	if n, _ := Process(r, at("2026-11-16 09:50"), msk); n != nil {
		t.Fatal("повторное уведомление")
	}
	n, _ = Process(r, at("2026-11-16 10:00"), msk)
	if n == nil || n.Offset != 0 || n.Missed {
		t.Fatalf("ожидалось уведомление в момент события, получено %+v", n)
	}
	if !r.Done {
		t.Fatal("разовое напоминание должно стать выполненным")
	}
}

func TestProcessLateOnlyOne(t *testing.T) {
	// Бот был выключен: наступили оба уведомления — приходит одно, о «пропущенном».
	r := &storage.Reminder{At: at("2026-11-16 10:00"), Offsets: []int{60, 0}}
	r.Start = r.At
	n, _ := Process(r, at("2026-11-16 11:00"), msk)
	if n == nil || n.Offset != 0 || !n.Missed {
		t.Fatalf("ожидалось одно пропущенное уведомление, получено %+v", n)
	}
	if !r.Done {
		t.Fatal("должно быть выполнено")
	}
}

func TestProcessTooLate(t *testing.T) {
	r := &storage.Reminder{At: at("2026-11-14 10:00"), Offsets: []int{0}}
	r.Start = r.At
	if n, _ := Process(r, at("2026-11-16 11:00"), msk); n != nil {
		t.Fatal("слишком старое напоминание не должно присылаться")
	}
	if !r.Done {
		t.Fatal("должно быть выполнено")
	}
}

func TestProcessRecurringCatchUp(t *testing.T) {
	// Ежедневное в 09:00, бот был выключен 3 дня; сейчас 08:50 — пропущенные
	// давно истекли, следующее вхождение сегодня в 09:00.
	r := &storage.Reminder{Repeat: storage.RepeatDaily, At: at("2026-11-13 09:00"), Offsets: []int{0}}
	r.Start = r.At
	n, _ := Process(r, at("2026-11-16 08:50"), msk)
	if n != nil {
		t.Fatalf("не ожидалось уведомления, получено %+v", n)
	}
	if !r.At.Equal(at("2026-11-16 09:00")) {
		t.Fatalf("At = %v, ожидалось 2026-11-16 09:00", r.At)
	}
	n, _ = Process(r, at("2026-11-16 09:00"), msk)
	if n == nil {
		t.Fatal("ожидалось уведомление")
	}
	if !r.At.Equal(at("2026-11-17 09:00")) {
		t.Fatalf("после срабатывания At = %v", r.At)
	}
}

func TestProcessAdvanceNoticeKeepsEventVisible(t *testing.T) {
	// Уведомление за 15 минут не должно сдвигать событие на следующую неделю
	// до того, как оно наступит.
	r := &storage.Reminder{Repeat: storage.RepeatWeekly, At: at("2026-11-16 10:00"), Offsets: []int{15}}
	r.Start = r.At
	if n, _ := Process(r, at("2026-11-16 09:46"), msk); n == nil {
		t.Fatal("ожидалось уведомление")
	}
	if !r.At.Equal(at("2026-11-16 10:00")) {
		t.Fatal("событие сдвинулось раньше времени")
	}
	Process(r, at("2026-11-16 10:00"), msk)
	if !r.At.Equal(at("2026-11-23 10:00")) {
		t.Fatalf("At = %v, ожидалось 23.11 10:00", r.At)
	}
}

func TestCompleteOccurrence(t *testing.T) {
	r := &storage.Reminder{Repeat: storage.RepeatDaily, At: at("2026-11-16 10:00"), Offsets: []int{30, 0}}
	r.Start = r.At
	ev := r.At
	if !CompleteOccurrence(r, ev, at("2026-11-16 09:31"), msk) {
		t.Fatal("ожидалось изменение")
	}
	if !r.At.Equal(at("2026-11-17 10:00")) {
		t.Fatalf("At = %v", r.At)
	}
	// Повторное нажатие на старой кнопке ничего не меняет.
	if CompleteOccurrence(r, ev, at("2026-11-16 09:32"), msk) {
		t.Fatal("старое вхождение не должно сдвигать серию")
	}
}

func TestAlignStart(t *testing.T) {
	r := &storage.Reminder{Repeat: storage.RepeatWeekdays, At: at("2026-11-21 08:00")} // суббота
	if !AlignStart(r, at("2026-11-16 12:00"), msk) || !r.At.Equal(at("2026-11-23 08:00")) {
		t.Fatalf("At = %v, ожидался понедельник 23.11", r.At)
	}
	r = &storage.Reminder{Repeat: storage.RepeatWeekly, At: at("2026-11-02 18:00")}
	if !AlignStart(r, at("2026-11-16 19:00"), msk) || !r.At.Equal(at("2026-11-23 18:00")) {
		t.Fatalf("At = %v", r.At)
	}
	r = &storage.Reminder{At: at("2026-11-02 18:00")}
	if AlignStart(r, at("2026-11-16 19:00"), msk) {
		t.Fatal("разовое в прошлом недопустимо")
	}
}

func TestOccurrences(t *testing.T) {
	r := &storage.Reminder{Repeat: storage.RepeatWeekdays, At: at("2026-11-16 08:00")}
	r.Start = r.At
	got := Occurrences(r, at("2026-11-16 00:00"), at("2026-11-23 00:00"), msk, 100)
	if len(got) != 5 {
		t.Fatalf("ожидалось 5 будних дней, получено %d", len(got))
	}
}

func TestDST(t *testing.T) {
	ny := mustLoc("America/New_York")
	start := time.Date(2026, 3, 7, 9, 0, 0, 0, ny)
	r := &storage.Reminder{Repeat: storage.RepeatDaily, Start: start, At: start}
	got, _ := NextOccurrence(r, start, ny)
	if got.In(ny).Hour() != 9 {
		t.Fatalf("после перехода на летнее время ожидалось 09:00, получено %v", got.In(ny))
	}
}
