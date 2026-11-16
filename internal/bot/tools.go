package bot

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/llm"
	"github.com/kkazantsevrv/ai-assistent/internal/schedule"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
)

// ---------- описание инструментов для модели ----------

type obj = map[string]any

func fn(name, desc string, props obj, required ...string) llm.Tool {
	params := obj{"type": "object", "properties": props}
	if len(required) > 0 {
		params["required"] = required
	}
	return llm.Tool{Type: "function", Function: llm.FunctionDef{Name: name, Description: desc, Parameters: params}}
}

var (
	pStr     = func(d string) obj { return obj{"type": "string", "description": d} }
	pInt     = func(d string) obj { return obj{"type": "integer", "description": d} }
	pIntList = func(d string) obj { return obj{"type": "array", "items": obj{"type": "integer"}, "description": d} }
	pRepeat  = obj{"type": "string", "enum": []string{"none", "daily", "weekly", "weekdays", "monthly", "yearly"},
		"description": "Повтор: none — разово, daily — каждый день, weekly — каждую неделю в тот же день, weekdays — по будням, monthly, yearly"}
	pBefore = pIntList("За сколько минут до события прислать уведомления. [0] — ровно в момент события. Пример: [60, 0] — за час и в момент события")
)

var toolDefs = []llm.Tool{
	fn("add_reminder", "Создать напоминание или событие расписания (в том числе повторяющееся).", obj{
		"text":                  pStr("Что напомнить / название события, коротко и понятно"),
		"datetime":              pStr("Локальные дата и время события: YYYY-MM-DDTHH:MM"),
		"in_minutes":            pInt("Вместо datetime: через сколько минут от текущего момента"),
		"remind_before_minutes": pBefore,
		"repeat":                pRepeat,
		"interval":              pInt("Каждые N дней/недель/месяцев (по умолчанию 1)"),
		"until":                 pStr("Последняя дата повторов: YYYY-MM-DD (необязательно)"),
		"notes":                 pStr("Подробности: место, адрес, ссылка, что взять с собой"),
		"group":                 pStr("Название расписания, к которому относится событие, например «Пары» или «Тренировки»"),
	}, "text"),
	fn("list_reminders", "Показать активные напоминания (с id). Можно отфильтровать по тексту.", obj{
		"query":        pStr("Подстрока для поиска в тексте, заметках или группе (необязательно)"),
		"include_done": obj{"type": "boolean", "description": "Включить выполненные/прошедшие"},
	}),
	fn("get_schedule", "Расписание на период с раскрытием повторяющихся событий: что запланировано по дням.", obj{
		"from": pStr("Первый день YYYY-MM-DD (по умолчанию сегодня)"),
		"to":   pStr("Последний день включительно YYYY-MM-DD (по умолчанию +6 дней)"),
	}),
	fn("update_reminder", "Изменить напоминание по id: текст, время, повтор, уведомления, заметки, группу.", obj{
		"id":                    pInt("id напоминания"),
		"text":                  pStr("Новый текст"),
		"datetime":              pStr("Новые дата и время YYYY-MM-DDTHH:MM (для повторяющихся — новое ближайшее вхождение)"),
		"remind_before_minutes": pBefore,
		"repeat":                pRepeat,
		"interval":              pInt("Каждые N единиц"),
		"until":                 pStr("Последняя дата повторов YYYY-MM-DD или «none», чтобы убрать"),
		"notes":                 pStr("Новые заметки"),
		"group":                 pStr("Новая группа"),
	}, "id"),
	fn("delete_reminders", "Удалить напоминания по списку id.", obj{
		"ids": pIntList("Список id"),
	}, "ids"),
	fn("complete_reminder", "Отметить выполненным: разовое закрывается, у повторяющегося пропускается текущее вхождение.", obj{
		"id": pInt("id напоминания"),
	}, "id"),
	fn("delete_group", "Удалить целиком расписание (все напоминания группы).", obj{
		"group": pStr("Название группы"),
	}, "group"),
	fn("update_group", "Изменить все напоминания группы: уведомления и/или название группы.", obj{
		"group":                 pStr("Текущее название группы"),
		"remind_before_minutes": pBefore,
		"new_name":              pStr("Новое название группы"),
	}, "group"),
	fn("add_note", "Сохранить заметку (информацию, которую нужно запомнить).", obj{
		"text": pStr("Текст заметки"),
		"tags": obj{"type": "array", "items": obj{"type": "string"}, "description": "Теги без # (необязательно)"},
	}, "text"),
	fn("list_notes", "Найти/показать заметки.", obj{
		"query": pStr("Подстрока для поиска в тексте или тегах (необязательно)"),
	}),
	fn("update_note", "Заменить текст заметки.", obj{
		"id":   pInt("id заметки"),
		"text": pStr("Новый полный текст"),
	}, "id", "text"),
	fn("delete_notes", "Удалить заметки по списку id.", obj{
		"ids": pIntList("Список id"),
	}, "ids"),
	fn("update_settings", "Изменить настройки пользователя.", obj{
		"timezone":              pStr("Часовой пояс IANA (Europe/Moscow, Asia/Novosibirsk) или смещение «+5»"),
		"remind_before_minutes": pInt("За сколько минут по умолчанию напоминать о событиях"),
		"digest_time":           pStr("Время утренней сводки ЧЧ:ММ или «off»"),
	}),
}

// ---------- разбор аргументов (модель иногда путает типы) ----------

type args map[string]any

func (a args) has(k string) bool {
	v, ok := a[k]
	return ok && v != nil
}

func (a args) str(k string) string {
	switch v := a[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (a args) int(k string) (int, bool) {
	switch v := a[k].(type) {
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(v, "#")))
		return n, err == nil
	case []any:
		if len(v) == 1 {
			return args{"x": v[0]}.int("x")
		}
	}
	return 0, false
}

func (a args) ints(k string) ([]int, bool) {
	switch v := a[k].(type) {
	case []any:
		var out []int
		for _, x := range v {
			if n, ok := (args{"x": x}).int("x"); ok {
				out = append(out, n)
			}
		}
		return out, true
	case float64:
		return []int{int(v)}, true
	case string:
		var out []int
		for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
			if n, err := strconv.Atoi(strings.TrimPrefix(p, "#")); err == nil {
				out = append(out, n)
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

func (a args) strs(k string) []string {
	var out []string
	switch v := a[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimPrefix(strings.TrimSpace(s), "#"))
			}
		}
	case string:
		for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
			out = append(out, strings.TrimPrefix(s, "#"))
		}
	}
	return out
}

// parseLocalTime понимает форматы, которые обычно присылает модель.
// Время без смещения (и с «Z» — модель часто добавляет его по привычке)
// считается локальным временем пользователя.
func parseLocalTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "Z")
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(loc), nil
	}
	for _, layout := range []string{
		"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02 15:04:05",
		"02.01.2006 15:04", "02.01.2006T15:04", "2006-01-02T15:04:05.000",
	} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{"2006-01-02", "02.01.2006"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.Add(9 * time.Hour), nil // дата без времени — 09:00
		}
	}
	return time.Time{}, fmt.Errorf("не понял дату/время %q, нужен формат YYYY-MM-DDTHH:MM", s)
}

func parseDate(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "02.01.2006", "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return dayStart(t), nil
		}
	}
	return time.Time{}, fmt.Errorf("не понял дату %q, нужен формат YYYY-MM-DD", s)
}

func endOfDay(t time.Time) time.Time {
	return dayStart(t).AddDate(0, 0, 1).Add(-time.Second)
}

func toJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func errResult(format string, a ...any) string {
	return toJSON(obj{"ok": false, "error": fmt.Sprintf(format, a...)})
}

// ---------- выполнение ----------

// execTool выполняет вызов функции моделью и возвращает результат в виде JSON.
func (b *Bot) execTool(chatID int64, name, rawArgs string) string {
	var a args
	if strings.TrimSpace(rawArgs) == "" {
		rawArgs = "{}"
	}
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return errResult("аргументы не являются корректным JSON: %v", err)
	}
	settings, loc := b.settings(chatID)
	now := time.Now().In(loc)

	var res string
	switch name {
	case "add_reminder":
		res = b.toolAddReminder(chatID, a, settings, loc, now)
	case "list_reminders":
		res = b.toolListReminders(chatID, a, loc, now)
	case "get_schedule":
		res = b.toolGetSchedule(chatID, a, loc, now)
	case "update_reminder":
		res = b.toolUpdateReminder(chatID, a, loc, now)
	case "delete_reminders":
		res = b.toolDeleteReminders(chatID, a)
	case "complete_reminder":
		res = b.toolComplete(chatID, a, loc, now)
	case "delete_group":
		res = b.toolDeleteGroup(chatID, a)
	case "update_group":
		res = b.toolUpdateGroup(chatID, a)
	case "add_note":
		res = b.toolAddNote(chatID, a)
	case "list_notes":
		res = b.toolListNotes(chatID, a, loc)
	case "update_note":
		res = b.toolUpdateNote(chatID, a)
	case "delete_notes":
		res = b.toolDeleteNotes(chatID, a)
	case "update_settings":
		res = b.toolUpdateSettings(chatID, a)
	default:
		res = errResult("неизвестная функция %s", name)
	}
	log.Printf("tool %s %s -> %s", name, truncateRunes(rawArgs, 300), truncateRunes(res, 300))
	return res
}

func describe(r *storage.Reminder, loc *time.Location, now time.Time) obj {
	o := obj{
		"id":            r.ID,
		"text":          r.Text,
		"datetime":      r.At.In(loc).Format("2006-01-02T15:04"),
		"when":          whenLabel(r.At.In(loc), now),
		"notifications": offsetsLabel(r.EffectiveOffsets()),
	}
	if rl := repeatLabel(r, loc); rl != "" {
		o["repeat"] = rl
	}
	if r.Until != nil {
		o["until"] = r.Until.In(loc).Format("2006-01-02")
	}
	if r.Notes != "" {
		o["notes"] = r.Notes
	}
	if r.Group != "" {
		o["group"] = r.Group
	}
	if r.Done {
		o["done"] = true
	}
	return o
}

func (b *Bot) toolAddReminder(chatID int64, a args, s storage.Settings, loc *time.Location, now time.Time) string {
	text := a.str("text")
	if text == "" {
		return errResult("не указан text")
	}
	var at time.Time
	if n, ok := a.int("in_minutes"); ok && n > 0 {
		at = now.Add(time.Duration(n) * time.Minute).Truncate(time.Second)
	} else if dt := a.str("datetime"); dt != "" {
		t, err := parseLocalTime(dt, loc)
		if err != nil {
			return errResult("%v", err)
		}
		at = t
	} else {
		return errResult("нужно указать datetime или in_minutes")
	}

	repeat, ok := storage.NormalizeRepeat(a.str("repeat"))
	if !ok {
		return errResult("неизвестный repeat %q", a.str("repeat"))
	}
	r := &storage.Reminder{
		ChatID: chatID, Text: text, Notes: a.str("notes"), Group: a.str("group"),
		At: at, Repeat: repeat, Offsets: []int{0}, CreatedAt: time.Now(), Source: "ai",
	}
	if iv, ok := a.int("interval"); ok && iv > 1 {
		r.Interval = iv
	}
	if u := a.str("until"); u != "" && repeat != storage.RepeatNone {
		t, err := parseDate(u, loc)
		if err != nil {
			return errResult("%v", err)
		}
		e := endOfDay(t)
		r.Until = &e
	}
	if offs, ok := a.ints("remind_before_minutes"); ok && len(offs) > 0 {
		r.Offsets = schedule.NormalizeOffsets(offs)
	}
	if !schedule.AlignStart(r, now, loc) {
		return errResult("время %s уже прошло (сейчас %s). Уточни у пользователя время или выбери будущее",
			at.Format("2006-01-02 15:04"), now.Format("2006-01-02 15:04"))
	}

	var out obj
	_ = b.st.Write(func(d *storage.Data) {
		// Защита от дублей, если модель повторила вызов.
		for _, ex := range d.ActiveReminders(chatID) {
			if strings.EqualFold(ex.Text, r.Text) && ex.At.Equal(r.At) && ex.Repeat == r.Repeat {
				out = describe(ex, loc, now)
				out["note"] = "такое напоминание уже было, новое не создано"
				return
			}
		}
		d.AddReminder(r)
		out = describe(r, loc, now)
	})
	out["ok"] = true
	return toJSON(out)
}

func matches(q string, fields ...string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

func (b *Bot) toolListReminders(chatID int64, a args, loc *time.Location, now time.Time) string {
	q := a.str("query")
	includeDone, _ := a["include_done"].(bool)
	var items []obj
	total := 0
	b.st.Read(func(d *storage.Data) {
		var list []*storage.Reminder
		if includeDone {
			for _, r := range d.Reminders {
				if r.ChatID == chatID {
					list = append(list, r)
				}
			}
		} else {
			list = d.ActiveReminders(chatID)
		}
		for _, r := range list {
			if !matches(q, r.Text, r.Notes, r.Group) {
				continue
			}
			total++
			if len(items) < 80 {
				items = append(items, describe(r, loc, now))
			}
		}
	})
	return toJSON(obj{"ok": true, "total": total, "reminders": items})
}

func (b *Bot) toolGetSchedule(chatID int64, a args, loc *time.Location, now time.Time) string {
	from := dayStart(now)
	if s := a.str("from"); s != "" {
		t, err := parseDate(s, loc)
		if err != nil {
			return errResult("%v", err)
		}
		from = t
	}
	to := from.AddDate(0, 0, 7)
	if s := a.str("to"); s != "" {
		t, err := parseDate(s, loc)
		if err != nil {
			return errResult("%v", err)
		}
		to = t.AddDate(0, 0, 1)
	}
	if to.Sub(from) > 62*24*time.Hour {
		to = from.AddDate(0, 0, 62)
	}
	if !to.After(from) {
		return errResult("to раньше from")
	}
	var lines []string
	b.st.Read(func(d *storage.Data) {
		for _, o := range b.agenda(d, chatID, from, to, loc) {
			line := fmt.Sprintf("%s %s %s — %s (#%d)", wdShort[o.At.Weekday()], o.At.Format("02.01"), o.At.Format("15:04"), o.Text, o.ID)
			if o.Notes != "" {
				line += " [" + truncateRunes(o.Notes, 100) + "]"
			}
			lines = append(lines, line)
			if len(lines) >= 300 {
				break
			}
		}
	})
	return toJSON(obj{
		"ok":     true,
		"period": from.Format("2006-01-02") + " — " + to.AddDate(0, 0, -1).Format("2006-01-02"),
		"events": lines,
		"count":  len(lines),
	})
}

func (b *Bot) toolUpdateReminder(chatID int64, a args, loc *time.Location, now time.Time) string {
	id, ok := a.int("id")
	if !ok {
		return errResult("не указан id")
	}
	var res string
	_ = b.st.WriteIf(func(d *storage.Data) bool {
		orig := d.Reminder(chatID, int64(id))
		if orig == nil {
			res = errResult("напоминание #%d не найдено", id)
			return false
		}
		r := *orig // меняем копию, чтобы при ошибке ничего не испортить
		r.Offsets = slices.Clone(orig.Offsets)
		r.Sent = slices.Clone(orig.Sent)

		if v := a.str("text"); v != "" {
			r.Text = v
		}
		if a.has("notes") {
			r.Notes = a.str("notes")
		}
		if a.has("group") {
			r.Group = a.str("group")
		}
		if a.has("repeat") {
			rep, ok := storage.NormalizeRepeat(a.str("repeat"))
			if !ok {
				res = errResult("неизвестный repeat %q", a.str("repeat"))
				return false
			}
			r.Repeat = rep
		}
		if iv, ok := a.int("interval"); ok {
			r.Interval = max(iv, 0)
		}
		if a.has("until") {
			u := a.str("until")
			if u == "" || strings.EqualFold(u, "none") || strings.EqualFold(u, "null") {
				r.Until = nil
			} else {
				t, err := parseDate(u, loc)
				if err != nil {
					res = errResult("%v", err)
					return false
				}
				e := endOfDay(t)
				r.Until = &e
			}
		}
		if offs, ok := a.ints("remind_before_minutes"); ok && len(offs) > 0 {
			r.Offsets = schedule.NormalizeOffsets(offs)
			var kept []int
			for _, s := range r.Sent {
				if slices.Contains(r.Offsets, s) {
					kept = append(kept, s)
				}
			}
			r.Sent = kept
		}
		timeChanged := false
		if dt := a.str("datetime"); dt != "" {
			t, err := parseLocalTime(dt, loc)
			if err != nil {
				res = errResult("%v", err)
				return false
			}
			r.At = t
			timeChanged = true
		}
		if timeChanged || a.has("repeat") || a.has("interval") {
			r.Sent = nil
			r.Done = false
			r.DoneAt = nil
			if !schedule.AlignStart(&r, now, loc) {
				res = errResult("время %s уже прошло (сейчас %s)", r.At.In(loc).Format("2006-01-02 15:04"), now.Format("2006-01-02 15:04"))
				return false
			}
		}
		*orig = r
		out := describe(orig, loc, now)
		out["ok"] = true
		res = toJSON(out)
		return true
	})
	return res
}

func (b *Bot) toolDeleteReminders(chatID int64, a args) string {
	ids, _ := a.ints("ids")
	if len(ids) == 0 {
		if id, ok := a.int("id"); ok {
			ids = []int{id}
		}
	}
	if len(ids) == 0 {
		return errResult("не указаны ids")
	}
	var deleted []string
	var missing []int
	_ = b.st.Write(func(d *storage.Data) {
		for _, id := range ids {
			if r := d.Reminder(chatID, int64(id)); r != nil {
				deleted = append(deleted, fmt.Sprintf("#%d %s", r.ID, r.Text))
				d.DeleteReminder(chatID, int64(id))
			} else {
				missing = append(missing, id)
			}
		}
	})
	return toJSON(obj{"ok": len(deleted) > 0, "deleted": deleted, "not_found": missing})
}

func (b *Bot) toolComplete(chatID int64, a args, loc *time.Location, now time.Time) string {
	id, ok := a.int("id")
	if !ok {
		return errResult("не указан id")
	}
	var res string
	_ = b.st.WriteIf(func(d *storage.Data) bool {
		r := d.Reminder(chatID, int64(id))
		if r == nil || r.Done {
			res = errResult("активное напоминание #%d не найдено", id)
			return false
		}
		schedule.CompleteOccurrence(r, time.Time{}, time.Now(), loc)
		out := describe(r, loc, now)
		out["ok"] = true
		if !r.Done {
			out["next"] = whenLabel(r.At.In(loc), now)
		}
		res = toJSON(out)
		return true
	})
	return res
}

func (b *Bot) toolDeleteGroup(chatID int64, a args) string {
	g := a.str("group")
	if g == "" {
		return errResult("не указана group")
	}
	n := 0
	_ = b.st.WriteIf(func(d *storage.Data) bool { n = d.DeleteGroup(chatID, g); return n > 0 })
	if n == 0 {
		return errResult("группа %q не найдена", g)
	}
	return toJSON(obj{"ok": true, "deleted": n})
}

func (b *Bot) toolUpdateGroup(chatID int64, a args) string {
	g := a.str("group")
	if g == "" {
		return errResult("не указана group")
	}
	offs, hasOffs := a.ints("remind_before_minutes")
	newName := a.str("new_name")
	n := 0
	_ = b.st.WriteIf(func(d *storage.Data) bool {
		for _, r := range d.ActiveReminders(chatID) {
			if !strings.EqualFold(r.Group, g) {
				continue
			}
			n++
			if hasOffs && len(offs) > 0 {
				r.Offsets = schedule.NormalizeOffsets(offs)
				r.Sent = nil
			}
			if newName != "" {
				r.Group = newName
			}
		}
		return n > 0
	})
	if n == 0 {
		return errResult("активных напоминаний в группе %q нет", g)
	}
	return toJSON(obj{"ok": true, "updated": n})
}

func (b *Bot) toolAddNote(chatID int64, a args) string {
	text := a.str("text")
	if text == "" {
		return errResult("пустой text")
	}
	var id int64
	_ = b.st.Write(func(d *storage.Data) {
		now := time.Now()
		id = d.AddNote(&storage.Note{ChatID: chatID, Text: text, Tags: a.strs("tags"), CreatedAt: now, UpdatedAt: now}).ID
	})
	return toJSON(obj{"ok": true, "id": id})
}

func (b *Bot) toolListNotes(chatID int64, a args, loc *time.Location) string {
	q := a.str("query")
	var items []obj
	total := 0
	b.st.Read(func(d *storage.Data) {
		notes := d.ChatNotes(chatID)
		for i := len(notes) - 1; i >= 0; i-- {
			n := notes[i]
			if !matches(q, append([]string{n.Text}, n.Tags...)...) {
				continue
			}
			total++
			if len(items) < 40 {
				items = append(items, obj{
					"id": n.ID, "date": n.CreatedAt.In(loc).Format("2006-01-02"),
					"text": truncateRunes(n.Text, 1500), "tags": n.Tags,
				})
			}
		}
	})
	return toJSON(obj{"ok": true, "total": total, "notes": items})
}

func (b *Bot) toolUpdateNote(chatID int64, a args) string {
	id, ok := a.int("id")
	text := a.str("text")
	if !ok || text == "" {
		return errResult("нужны id и text")
	}
	found := false
	_ = b.st.WriteIf(func(d *storage.Data) bool {
		if n := d.Note(chatID, int64(id)); n != nil {
			n.Text = text
			n.UpdatedAt = time.Now()
			found = true
		}
		return found
	})
	if !found {
		return errResult("заметка #%d не найдена", id)
	}
	return toJSON(obj{"ok": true, "id": id})
}

func (b *Bot) toolDeleteNotes(chatID int64, a args) string {
	ids, _ := a.ints("ids")
	if len(ids) == 0 {
		return errResult("не указаны ids")
	}
	var deleted, missing []int
	_ = b.st.Write(func(d *storage.Data) {
		for _, id := range ids {
			if d.DeleteNote(chatID, int64(id)) {
				deleted = append(deleted, id)
			} else {
				missing = append(missing, id)
			}
		}
	})
	return toJSON(obj{"ok": len(deleted) > 0, "deleted": deleted, "not_found": missing})
}

func (b *Bot) toolUpdateSettings(chatID int64, a args) string {
	var tz, digest string
	before := -1
	if v := a.str("timezone"); v != "" {
		var ok bool
		if tz, ok = parseTimezone(v); !ok {
			return errResult("неизвестный часовой пояс %q", v)
		}
	}
	if n, ok := a.int("remind_before_minutes"); ok {
		if n < 0 || n > 7*24*60 {
			return errResult("remind_before_minutes вне диапазона")
		}
		before = n
	}
	if v := strings.ToLower(a.str("digest_time")); v != "" {
		if v == "off" || v == "none" || v == "выкл" {
			digest = "off"
		} else if t, err := time.Parse("15:04", v); err == nil {
			digest = t.Format("15:04")
		} else {
			return errResult("digest_time: нужен формат ЧЧ:ММ или off")
		}
	}
	var s storage.Settings
	_ = b.st.Write(func(d *storage.Data) {
		c := d.Chat(chatID, b.defaultSettings(), time.Now())
		if tz != "" {
			c.Settings.Timezone = tz
		}
		if before >= 0 {
			c.Settings.RemindBefore = before
		}
		switch digest {
		case "":
		case "off":
			c.Settings.DigestTime = ""
		default:
			c.Settings.DigestTime = digest
		}
		s = c.Settings
	})
	return toJSON(obj{"ok": true, "settings": s})
}
