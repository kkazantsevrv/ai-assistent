package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/schedule"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
	"github.com/kkazantsevrv/ai-assistent/internal/telegram"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

const helpText = `<b>👋 Привет! Я твой личный ИИ-ассистент.</b>

Пиши мне обычным языком — я пойму:
• «Напомни завтра в 9 позвонить маме»
• «Каждый понедельник в 19:00 тренировка, напоминай за час»
• «Через 20 минут выключить духовку»
• «Запиши заметку: пароль от Wi-Fi на даче — 12345678»
• «Что у меня на пятницу?» / «Перенеси встречу на 16:00»
• «Удали все напоминания про спортзал»

📎 <b>Файлы с расписанием</b> — присылай .txt, .docx, .xlsx или .csv. Я найду в них события, покажу список и после подтверждения поставлю напоминания. В подписи к файлу можно уточнить, например: «напоминай за 10 минут».

💬 Можно и просто поговорить или обсудить любой вопрос — я помню контекст беседы.

<b>Команды</b>
/today, /tomorrow, /week — план
/list — все активные напоминания
/remind 30m текст · /remind 18:30 текст · /remind 25.12 10:00 текст
/notes — заметки, /note текст — новая заметка
/settings — часовой пояс, за сколько напоминать, утренняя сводка
/reset — забыть историю диалога
/export — выгрузить данные`

var reCmdWithID = regexp.MustCompile(`^(del|done|delnote)_(\d+)$`)

// handleCommand обрабатывает /команды. Возвращает false, если команда не
// распознана (тогда текст уходит ИИ как обычное сообщение).
func (b *Bot) handleCommand(ctx context.Context, m *telegram.Message, text string) bool {
	chatID := m.Chat.ID
	first, args, _ := strings.Cut(text, " ")
	args = strings.TrimSpace(args)
	cmd := strings.ToLower(strings.TrimPrefix(first, "/"))
	if i := strings.Index(cmd, "@"); i >= 0 {
		cmd = cmd[:i]
	}

	if mm := reCmdWithID.FindStringSubmatch(cmd); mm != nil {
		cmd = mm[1]
		args = mm[2]
	}

	switch cmd {
	case "start", "help":
		b.settings(chatID) // создаём состояние чата
		b.sendHTML(ctx, chatID, helpText, nil)
	case "list", "reminders", "all":
		b.cmdList(ctx, chatID)
	case "today":
		b.cmdAgenda(ctx, chatID, 0, 1)
	case "tomorrow":
		b.cmdAgenda(ctx, chatID, 1, 1)
	case "week", "schedule":
		b.cmdAgenda(ctx, chatID, 0, 7)
	case "notes":
		b.cmdNotes(ctx, chatID)
	case "note":
		b.cmdAddNote(ctx, chatID, args)
	case "remind", "r":
		b.cmdRemind(ctx, chatID, args)
	case "del", "delete":
		b.cmdDelete(ctx, chatID, args)
	case "done":
		b.cmdDone(ctx, chatID, args)
	case "delnote":
		b.cmdDeleteNote(ctx, chatID, args)
	case "settings":
		b.cmdSettings(ctx, chatID)
	case "tz", "timezone":
		b.cmdTimezone(ctx, chatID, args)
	case "before":
		b.cmdBefore(ctx, chatID, args)
	case "digest":
		b.cmdDigest(ctx, chatID, args)
	case "reset", "clear", "new":
		_ = b.st.Write(func(d *storage.Data) {
			d.Chat(chatID, b.defaultSettings(), time.Now()).History = nil
		})
		b.sendPlain(ctx, chatID, "🧹 История диалога очищена. Напоминания и заметки на месте.")
	case "export":
		b.cmdExport(ctx, chatID)
	case "id":
		b.sendPlain(ctx, chatID, fmt.Sprintf("Ваш Telegram ID: %d\nID чата: %d", m.From.ID, chatID))
	default:
		return false
	}
	return true
}

// ---------- списки ----------

func (b *Bot) cmdList(ctx context.Context, chatID int64) {
	_, loc := b.settings(chatID)
	now := time.Now().In(loc)
	var sb strings.Builder
	count := 0
	b.st.Read(func(d *storage.Data) {
		list := d.ActiveReminders(chatID)
		count = len(list)
		lastDay := ""
		for i, r := range list {
			if i >= 100 {
				fmt.Fprintf(&sb, "\n…и ещё %d", len(list)-i)
				break
			}
			at := r.At.In(loc)
			if day := dayLabel(at, now); day != lastDay {
				fmt.Fprintf(&sb, "\n<b>%s</b>\n", esc(capitalize(day)))
				lastDay = day
			}
			sb.WriteString(reminderLine(r, loc, true))
		}
	})
	if count == 0 {
		b.sendPlain(ctx, chatID, "Активных напоминаний нет. Напиши, например: «напомни завтра в 10 купить хлеб».")
		return
	}
	b.sendHTML(ctx, chatID, fmt.Sprintf("<b>📋 Активные напоминания (%d)</b>\n%s", count, sb.String()), nil)
}

// reminderLine — строка списка с деталями и быстрыми командами.
func reminderLine(r *storage.Reminder, loc *time.Location, withCmds bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "⏰ <b>%s</b> %s <i>#%d</i>\n", r.At.In(loc).Format("15:04"), esc(r.Text), r.ID)
	var meta []string
	if rl := repeatLabel(r, loc); rl != "" {
		meta = append(meta, "🔁 "+rl)
	}
	meta = append(meta, "🔔 "+offsetsLabel(r.EffectiveOffsets()))
	if r.Group != "" {
		meta = append(meta, "📁 "+esc(r.Group))
	}
	fmt.Fprintf(&sb, "      %s\n", strings.Join(meta, " · "))
	if r.Notes != "" {
		fmt.Fprintf(&sb, "      📝 %s\n", esc(truncateRunes(r.Notes, 200)))
	}
	if withCmds {
		fmt.Fprintf(&sb, "      /done_%d · /del_%d\n", r.ID, r.ID)
	}
	return sb.String()
}

func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

type occurrence struct {
	At       time.Time
	Text     string
	Notes    string
	ID       int64
	Repeated bool
}

// agenda собирает вхождения всех активных напоминаний в интервале.
func (b *Bot) agenda(d *storage.Data, chatID int64, from, to time.Time, loc *time.Location) []occurrence {
	var out []occurrence
	for _, r := range d.ActiveReminders(chatID) {
		for _, t := range schedule.Occurrences(r, from, to, loc, 200) {
			out = append(out, occurrence{At: t.In(loc), Text: r.Text, Notes: r.Notes, ID: r.ID, Repeated: r.Repeat != ""})
		}
	}
	sortOccurrences(out)
	return out
}

func sortOccurrences(o []occurrence) {
	sort.SliceStable(o, func(i, j int) bool { return o[i].At.Before(o[j].At) })
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func (b *Bot) cmdAgenda(ctx context.Context, chatID int64, fromDay, days int) {
	_, loc := b.settings(chatID)
	now := time.Now().In(loc)
	start := dayStart(now).AddDate(0, 0, fromDay)

	var sb strings.Builder
	total := 0
	b.st.Read(func(d *storage.Data) {
		for i := 0; i < days; i++ {
			from := start.AddDate(0, 0, i)
			to := from.AddDate(0, 0, 1)
			occ := b.agenda(d, chatID, from, to, loc)
			if len(occ) == 0 && days > 1 {
				continue
			}
			fmt.Fprintf(&sb, "<b>🗓 %s</b>\n", esc(capitalize(dayLabel(from, now))))
			if len(occ) == 0 {
				sb.WriteString("Ничего не запланировано ✨\n")
			}
			for _, o := range occ {
				mark := ""
				if o.At.Before(now) {
					mark = "✔️ "
				}
				fmt.Fprintf(&sb, "%s<b>%s</b> %s", mark, o.At.Format("15:04"), esc(o.Text))
				if o.Notes != "" {
					fmt.Fprintf(&sb, " — <i>%s</i>", esc(truncateRunes(o.Notes, 120)))
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
			total += len(occ)
		}
	})
	if days > 1 && total == 0 {
		b.sendPlain(ctx, chatID, "На ближайшую неделю ничего не запланировано ✨")
		return
	}
	b.sendHTML(ctx, chatID, strings.TrimSpace(sb.String()), nil)
}

// ---------- заметки ----------

func (b *Bot) cmdNotes(ctx context.Context, chatID int64) {
	_, loc := b.settings(chatID)
	var sb strings.Builder
	count := 0
	b.st.Read(func(d *storage.Data) {
		notes := d.ChatNotes(chatID)
		count = len(notes)
		start := max(len(notes)-50, 0)
		for i := len(notes) - 1; i >= start; i-- {
			n := notes[i]
			fmt.Fprintf(&sb, "<b>#%d</b> · %s", n.ID, n.CreatedAt.In(loc).Format("02.01.2006"))
			if len(n.Tags) > 0 {
				sb.WriteString(" · " + esc("#"+strings.Join(n.Tags, " #")))
			}
			fmt.Fprintf(&sb, "\n%s\n/delnote_%d\n\n", esc(truncateRunes(n.Text, 600)), n.ID)
		}
		if start > 0 {
			fmt.Fprintf(&sb, "…и ещё %d более старых (спроси меня, найду)", start)
		}
	})
	if count == 0 {
		b.sendPlain(ctx, chatID, "Заметок пока нет. Добавь: /note текст — или просто скажи «запиши…».")
		return
	}
	b.sendHTML(ctx, chatID, fmt.Sprintf("<b>📝 Заметки (%d)</b>\n\n%s", count, strings.TrimSpace(sb.String())), nil)
}

func (b *Bot) cmdAddNote(ctx context.Context, chatID int64, text string) {
	if text == "" {
		b.sendPlain(ctx, chatID, "Напиши так: /note текст заметки")
		return
	}
	var id int64
	_ = b.st.Write(func(d *storage.Data) {
		now := time.Now()
		id = d.AddNote(&storage.Note{ChatID: chatID, Text: text, CreatedAt: now, UpdatedAt: now}).ID
	})
	b.sendPlain(ctx, chatID, fmt.Sprintf("📝 Заметка #%d сохранена.", id))
}

func (b *Bot) cmdDeleteNote(ctx context.Context, chatID int64, args string) {
	id, err := strconv.ParseInt(strings.TrimPrefix(args, "#"), 10, 64)
	if err != nil {
		b.sendPlain(ctx, chatID, "Укажи номер: /delnote 5")
		return
	}
	ok := false
	_ = b.st.WriteIf(func(d *storage.Data) bool { ok = d.DeleteNote(chatID, id); return ok })
	if ok {
		b.sendPlain(ctx, chatID, fmt.Sprintf("🗑 Заметка #%d удалена.", id))
	} else {
		b.sendPlain(ctx, chatID, fmt.Sprintf("Заметка #%d не найдена.", id))
	}
}

// ---------- напоминания ----------

func (b *Bot) cmdRemind(ctx context.Context, chatID int64, args string) {
	if args == "" {
		b.sendHTML(ctx, chatID, "Примеры:\n<code>/remind 30m проверить почту</code>\n<code>/remind 2h30m забрать посылку</code>\n"+
			"<code>/remind 18:30 позвонить</code>\n<code>/remind завтра 9:00 спортзал</code>\n<code>/remind 25.12 10:00 подарки</code>\n\n"+
			"Или просто напиши обычным текстом — я разберусь.", nil)
		return
	}
	_, loc := b.settings(chatID)
	now := time.Now().In(loc)
	at, text, ok := parseQuickRemind(args, now)
	if !ok {
		if b.ai != nil {
			b.chat(ctx, chatID, "Напомни: "+args, "/remind "+args)
			return
		}
		b.sendPlain(ctx, chatID, "Не понял время. Пример: /remind 30m текст или /remind 18:30 текст")
		return
	}
	var r storage.Reminder
	_ = b.st.Write(func(d *storage.Data) {
		r = *d.AddReminder(&storage.Reminder{
			ChatID: chatID, Text: text, Start: at, At: at, Offsets: []int{0},
			CreatedAt: time.Now(), Source: "command",
		})
	})
	b.sendHTML(ctx, chatID, fmt.Sprintf("✅ Напомню <b>%s</b> (через %s):\n%s <i>#%d</i>",
		esc(whenLabel(at, now)), humanDuration(at.Sub(now)), esc(text), r.ID), nil)
}

func parseID(args string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(args), "#"), 10, 64)
	return id, err == nil
}

func (b *Bot) cmdDelete(ctx context.Context, chatID int64, args string) {
	id, ok := parseID(args)
	if !ok {
		b.sendPlain(ctx, chatID, "Укажи номер: /del 12 (номера — в /list)")
		return
	}
	var text string
	_ = b.st.WriteIf(func(d *storage.Data) bool {
		if r := d.Reminder(chatID, id); r != nil {
			text = r.Text
			return d.DeleteReminder(chatID, id)
		}
		return false
	})
	if text == "" {
		b.sendPlain(ctx, chatID, fmt.Sprintf("Напоминание #%d не найдено.", id))
		return
	}
	b.sendPlain(ctx, chatID, fmt.Sprintf("🗑 Удалено #%d: %s", id, text))
}

func (b *Bot) cmdDone(ctx context.Context, chatID int64, args string) {
	id, ok := parseID(args)
	if !ok {
		b.sendPlain(ctx, chatID, "Укажи номер: /done 12")
		return
	}
	_, loc := b.settings(chatID)
	msg := ""
	_ = b.st.WriteIf(func(d *storage.Data) bool {
		r := d.Reminder(chatID, id)
		if r == nil || r.Done {
			return false
		}
		schedule.CompleteOccurrence(r, time.Time{}, time.Now(), loc)
		if r.Done {
			msg = fmt.Sprintf("✅ Выполнено: %s", r.Text)
		} else {
			msg = fmt.Sprintf("✅ Отмечено. Следующий раз: %s — %s", whenLabel(r.At.In(loc), time.Now().In(loc)), r.Text)
		}
		return true
	})
	if msg == "" {
		msg = fmt.Sprintf("Активное напоминание #%d не найдено.", id)
	}
	b.sendPlain(ctx, chatID, msg)
}

// ---------- настройки ----------

func (b *Bot) cmdSettings(ctx context.Context, chatID int64) {
	s, loc := b.settings(chatID)
	digest := "выключена"
	if s.DigestTime != "" {
		digest = s.DigestTime
	}
	model := "не подключена (нет DEEPSEEK_API_KEY)"
	if b.ai != nil {
		model = b.ai.Model
	}
	text := fmt.Sprintf(`<b>⚙️ Настройки</b>
🌍 Часовой пояс: <b>%s</b> (сейчас %s)
🔔 О событиях напоминаю: <b>%s</b>
☀️ Утренняя сводка: <b>%s</b>
🤖 Модель ИИ: %s

<b>Изменить</b>
<code>/tz Europe/Moscow</code> или <code>/tz +5</code>
<code>/before 30</code> — за сколько минут напоминать о событиях
<code>/digest 07:30</code> или <code>/digest off</code>

Или просто скажи: «напоминай за полчаса», «я теперь в Новосибирске».`,
		esc(s.Timezone), time.Now().In(loc).Format("15:04"), esc(offsetLabel(s.RemindBefore)), digest, esc(model))
	b.sendHTML(ctx, chatID, text, nil)
}

var reUTCOffset = regexp.MustCompile(`(?i)^(?:utc|gmt|мск)?\s*([+-])\s*(\d{1,2})$`)

// parseTimezone принимает IANA-имя («Europe/Moscow»), смещение («+5», «UTC+3») или «мск».
func parseTimezone(s string) (string, bool) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "мск", "msk", "москва", "moscow":
		return "Europe/Moscow", true
	case "utc", "gmt":
		return "UTC", true
	}
	if m := reUTCOffset.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[2])
		if m[1] == "-" {
			n = -n
		}
		if strings.HasPrefix(strings.ToLower(s), "мск") {
			n += 3 // «МСК+2» = UTC+5
		}
		if n < -12 || n > 14 {
			return "", false
		}
		if n == 0 {
			return "UTC", true
		}
		// В базе tz знаки у Etc/GMT инвертированы: Etc/GMT-3 = UTC+3.
		return fmt.Sprintf("Etc/GMT%+d", -n), true
	}
	if _, err := time.LoadLocation(s); err == nil && s != "" && s != "Local" {
		return s, true
	}
	return "", false
}

func (b *Bot) updateSettings(chatID int64, fn func(s *storage.Settings)) {
	_ = b.st.Write(func(d *storage.Data) {
		fn(&d.Chat(chatID, b.defaultSettings(), time.Now()).Settings)
	})
}

func (b *Bot) cmdTimezone(ctx context.Context, chatID int64, args string) {
	tz, ok := parseTimezone(args)
	if !ok {
		b.sendHTML(ctx, chatID, "Не знаю такой часовой пояс. Примеры: <code>/tz Europe/Moscow</code>, <code>/tz Asia/Yekaterinburg</code>, <code>/tz +7</code>", nil)
		return
	}
	b.updateSettings(chatID, func(s *storage.Settings) { s.Timezone = tz })
	b.sendPlain(ctx, chatID, fmt.Sprintf("🌍 Часовой пояс: %s, сейчас там %s.", tz, time.Now().In(b.location(tz)).Format("15:04")))
}

func (b *Bot) cmdBefore(ctx context.Context, chatID int64, args string) {
	n, err := strconv.Atoi(strings.TrimSpace(args))
	if err != nil || n < 0 || n > 7*24*60 {
		b.sendPlain(ctx, chatID, "Укажи число минут: /before 15 (0 — в момент события)")
		return
	}
	b.updateSettings(chatID, func(s *storage.Settings) { s.RemindBefore = n })
	b.sendPlain(ctx, chatID, "🔔 О новых событиях буду напоминать "+offsetLabel(n)+".")
}

func (b *Bot) cmdDigest(ctx context.Context, chatID int64, args string) {
	args = strings.ToLower(strings.TrimSpace(args))
	switch args {
	case "off", "выкл", "нет", "0":
		b.updateSettings(chatID, func(s *storage.Settings) { s.DigestTime = "" })
		b.sendPlain(ctx, chatID, "☀️ Утренняя сводка выключена.")
		return
	}
	t, err := time.Parse("15:04", strings.ReplaceAll(args, ".", ":"))
	if err != nil {
		b.sendPlain(ctx, chatID, "Формат: /digest 08:00 или /digest off")
		return
	}
	hm := t.Format("15:04")
	b.updateSettings(chatID, func(s *storage.Settings) { s.DigestTime = hm })
	b.sendPlain(ctx, chatID, "☀️ Буду присылать план на день в "+hm+".")
}

// ---------- экспорт ----------

func (b *Bot) cmdExport(ctx context.Context, chatID int64) {
	type export struct {
		ExportedAt time.Time           `json:"exported_at"`
		Settings   storage.Settings    `json:"settings"`
		Reminders  []*storage.Reminder `json:"reminders"`
		Notes      []*storage.Note     `json:"notes"`
	}
	var raw []byte
	b.st.Read(func(d *storage.Data) {
		e := export{ExportedAt: time.Now(), Notes: d.ChatNotes(chatID)}
		if c, ok := d.Chats[chatID]; ok {
			e.Settings = c.Settings
		}
		for _, r := range d.Reminders {
			if r.ChatID == chatID {
				e.Reminders = append(e.Reminders, r)
			}
		}
		raw, _ = json.MarshalIndent(e, "", "  ")
	})
	name := "assistant-export-" + time.Now().Format("2006-01-02") + ".json"
	if err := b.tg.SendDocument(ctx, chatID, name, raw, "📦 Ваши напоминания, заметки и настройки"); err != nil {
		b.sendPlain(ctx, chatID, "Не удалось отправить файл: "+err.Error())
	}
}
