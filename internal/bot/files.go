package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/fileparse"
	"github.com/kkazantsevrv/ai-assistent/internal/llm"
	"github.com/kkazantsevrv/ai-assistent/internal/schedule"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
	"github.com/kkazantsevrv/ai-assistent/internal/telegram"
)

const (
	maxFileBytes     = 20 << 20 // лимит Bot API на скачивание
	maxFileCharsLLM  = 60000    // сколько текста файла отдаём модели
	maxFileCharsHist = 6000     // сколько текста файла сохраняем в историю диалога
)

type importItem struct {
	Title    string
	Notes    string
	At       time.Time
	Start    time.Time
	Repeat   string
	Interval int
	Until    *time.Time
	Offsets  []int
	NoTime   bool
}

type pendingImport struct {
	ChatID   int64
	Filename string
	Group    string
	Items    []importItem
	Created  time.Time
}

func (b *Bot) handleDocument(ctx context.Context, m *telegram.Message) {
	chatID := m.Chat.ID
	doc := m.Document
	name := doc.FileName
	if name == "" {
		name = "файл"
	}
	if doc.FileSize > maxFileBytes {
		b.sendPlain(ctx, chatID, "📎 Файл больше 20 МБ — Telegram не даёт ботам скачивать такие. Пришли файл поменьше.")
		return
	}

	stop := b.typing(ctx, chatID)
	defer stop()

	f, err := b.tg.GetFile(ctx, doc.FileID)
	if err != nil {
		b.sendPlain(ctx, chatID, "Не удалось получить файл: "+err.Error())
		return
	}
	data, err := b.tg.DownloadFile(ctx, f.FilePath, maxFileBytes)
	if err != nil {
		b.sendPlain(ctx, chatID, "Не удалось скачать файл: "+err.Error())
		return
	}

	text, err := fileparse.Extract(name, doc.MimeType, data)
	if err != nil {
		var le *fileparse.LegacyError
		switch {
		case errors.As(err, &le):
			b.sendPlain(ctx, chatID, "📎 "+capitalize(le.Error())+" (Файл → Сохранить как).")
		case errors.Is(err, fileparse.ErrUnsupported):
			b.sendPlain(ctx, chatID, "📎 Такой формат не умею читать. Поддерживаю: .txt, .md, .csv, .docx, .xlsx (и другие текстовые файлы).")
		default:
			b.sendPlain(ctx, chatID, "📎 Не удалось прочитать файл: "+err.Error())
		}
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		b.sendPlain(ctx, chatID, "📎 В файле не нашлось текста.")
		return
	}
	caption := strings.TrimSpace(m.Caption)

	if b.ai == nil {
		b.sendPlain(ctx, chatID, "📎 Текст файла прочитан, но ИИ не подключён (нет DEEPSEEK_API_KEY), поэтому разобрать расписание не могу.\n\n"+
			truncateRunes(text, 1500))
		return
	}

	forLLM := truncateRunes(text, maxFileCharsLLM)
	if len([]rune(text)) > maxFileCharsLLM {
		forLLM += "\n[файл обрезан — показана только первая часть]"
	}

	res, err := b.extractSchedule(ctx, chatID, name, caption, forLLM)
	if err != nil {
		log.Printf("извлечение расписания: %v", err)
	}
	if err == nil && res.IsSchedule && len(res.Items) > 0 {
		stop()
		b.offerImport(ctx, chatID, name, caption, text, res)
		return
	}

	// Не расписание — обсуждаем файл как обычное сообщение.
	ask := caption
	if ask == "" {
		ask = "Кратко расскажи, что в этом файле. Если там есть задачи или даты — предложи поставить напоминания."
	}
	user := fmt.Sprintf("Я прислал файл «%s».\n\nСодержимое файла:\n<<<\n%s\n>>>\n\n%s", name, forLLM, ask)
	hist := fmt.Sprintf("Я прислал файл «%s».\n\nСодержимое файла (начало):\n<<<\n%s\n>>>\n\n%s", name, truncateRunes(text, maxFileCharsHist), ask)
	stop()
	b.chat(ctx, chatID, user, hist)
}

// ---------- извлечение расписания моделью ----------

type extractedItem struct {
	Title        string          `json:"title"`
	Date         string          `json:"date"`
	Weekday      string          `json:"weekday"`
	Time         string          `json:"time"`
	EndTime      string          `json:"end_time"`
	Repeat       string          `json:"repeat"`
	Interval     json.RawMessage `json:"interval"`
	Until        string          `json:"until"`
	RemindBefore json.RawMessage `json:"remind_before_minutes"`
	Notes        string          `json:"notes"`
}

type extractResult struct {
	IsSchedule bool            `json:"is_schedule"`
	Summary    string          `json:"summary"`
	Group      string          `json:"group"`
	Items      []extractedItem `json:"items"`
}

func (b *Bot) extractSchedule(ctx context.Context, chatID int64, filename, caption, content string) (*extractResult, error) {
	s, loc := b.settings(chatID)
	now := time.Now().In(loc)

	var sb strings.Builder
	fmt.Fprintf(&sb, `Ты — модуль извлечения расписания. Тебе дают текст файла (таблица Excel, документ Word или текст) и, возможно, комментарий пользователя. Найди события, занятия, смены, задачи и дедлайны, на которые стоит поставить напоминания.

Сейчас: %s, %s %s (часовой пояс %s).
Календарь:
`, wdFull[now.Weekday()], now.Format("2006-01-02"), now.Format("15:04"), s.Timezone)
	for i := 0; i < 14; i++ {
		d := now.AddDate(0, 0, i)
		fmt.Fprintf(&sb, "%s %s\n", wdShort[d.Weekday()], d.Format("2006-01-02"))
	}
	fmt.Fprintf(&sb, `По умолчанию пользователь хочет напоминание за %d мин до события.

Ответь строго одним JSON-объектом (формат json):
{
  "is_schedule": true,
  "summary": "о чём файл, 1–2 предложения",
  "group": "короткое название расписания: «Пары», «Смены», «Тренировки», «Дедлайны»…",
  "items": [
    {
      "title": "что за событие — кратко, с важным: предмет, тип занятия, с кем",
      "date": "YYYY-MM-DD, если указана конкретная дата, иначе пусто",
      "weekday": "mon|tue|wed|thu|fri|sat|sun, если указан только день недели, иначе пусто",
      "time": "HH:MM начала или пусто",
      "end_time": "HH:MM окончания или пусто",
      "repeat": "none|daily|weekly|weekdays|monthly|yearly",
      "interval": 1,
      "until": "YYYY-MM-DD или пусто",
      "remind_before_minutes": [],
      "notes": "место, аудитория, преподаватель, ссылка и т.п."
    }
  ]
}

Правила:
- День недели без конкретной даты — еженедельное событие: weekday + repeat "weekly".
- «Числитель/знаменатель», «чётная/нечётная неделя» — interval 2, пометь в notes.
- Конкретные даты без повторов — repeat "none". Год не указан — ближайшая будущая дата.
- Комментарий пользователя важнее правил по умолчанию («напоминай за 10 минут» → remind_before_minutes [10], «только понедельник» → только понедельник). Если пользователь ничего не сказал — оставь remind_before_minutes пустым.
- Не выдумывай события, которых нет в файле. Заголовки, пустые ячейки, перемены — не события.
- Если в файле нет событий с датами, днями недели или временем (статья, письмо, заметки) — is_schedule false, items пустой.
- Не больше 150 событий.
`, s.RemindBefore)

	captionText := caption
	if captionText == "" {
		captionText = "(нет)"
	}
	user := fmt.Sprintf("Файл: %s\nКомментарий пользователя: %s\n\nСодержимое файла:\n<<<\n%s\n>>>", filename, captionText, content)

	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	resp, _, err := b.ai.Chat(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: sb.String()},
			{Role: "user", Content: user},
		},
		Temperature:    llm.Float(0.1),
		MaxTokens:      8000,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(resp.Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var res extractResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("модель вернула некорректный JSON: %w", err)
	}
	return &res, nil
}

var weekdayNames = map[string]time.Weekday{
	"mon": time.Monday, "monday": time.Monday, "пн": time.Monday, "понедельник": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday, "вт": time.Tuesday, "вторник": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday, "ср": time.Wednesday, "среда": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday, "чт": time.Thursday, "четверг": time.Thursday,
	"fri": time.Friday, "friday": time.Friday, "пт": time.Friday, "пятница": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday, "сб": time.Saturday, "суббота": time.Saturday,
	"sun": time.Sunday, "sunday": time.Sunday, "вс": time.Sunday, "воскресенье": time.Sunday,
}

func parseClock(s string) (h, m int, ok bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ".", ":"))
	if s == "" {
		return 0, 0, false
	}
	t, err := time.Parse("15:04", s)
	if err != nil {
		if t, err = time.Parse("15:04:05", s); err != nil {
			return 0, 0, false
		}
	}
	return t.Hour(), t.Minute(), true
}

func rawInts(raw json.RawMessage) []int {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	out, _ := args{"x": v}.ints("x")
	return out
}

// convertItems превращает ответ модели в готовые к добавлению напоминания.
func convertItems(res *extractResult, s storage.Settings, loc *time.Location, now time.Time) (items []importItem, skippedPast, skippedBad int) {
	for _, it := range res.Items {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			skippedBad++
			continue
		}
		h, mi, hasTime := parseClock(it.Time)
		if !hasTime {
			h, mi = 9, 0
		}
		repeat, ok := storage.NormalizeRepeat(it.Repeat)
		if !ok {
			repeat = storage.RepeatNone
		}

		var at time.Time
		switch {
		case strings.TrimSpace(it.Date) != "":
			d, err := parseDate(it.Date, loc)
			if err != nil {
				skippedBad++
				continue
			}
			at = time.Date(d.Year(), d.Month(), d.Day(), h, mi, 0, 0, loc)
		case strings.TrimSpace(it.Weekday) != "":
			wd, ok := weekdayNames[strings.ToLower(strings.TrimSpace(it.Weekday))]
			if !ok {
				skippedBad++
				continue
			}
			at = time.Date(now.Year(), now.Month(), now.Day(), h, mi, 0, 0, loc)
			at = at.AddDate(0, 0, (int(wd)-int(at.Weekday())+7)%7)
			if !at.After(now) {
				at = at.AddDate(0, 0, 7)
			}
			if repeat == storage.RepeatNone {
				repeat = storage.RepeatWeekly
			}
		default:
			skippedBad++
			continue
		}

		notes := strings.TrimSpace(it.Notes)
		if eh, em, ok := parseClock(it.EndTime); ok && hasTime {
			end := fmt.Sprintf("до %02d:%02d", eh, em)
			if notes == "" {
				notes = end
			} else {
				notes = end + "; " + notes
			}
		}

		r := storage.Reminder{At: at, Repeat: repeat}
		if ivs := rawInts(it.Interval); len(ivs) > 0 && ivs[0] > 1 {
			r.Interval = ivs[0]
		}
		if u := strings.TrimSpace(it.Until); u != "" && repeat != storage.RepeatNone {
			if t, err := parseDate(u, loc); err == nil {
				e := endOfDay(t)
				r.Until = &e
			}
		}
		if !schedule.AlignStart(&r, now, loc) {
			skippedPast++
			continue
		}

		offs := rawInts(it.RemindBefore)
		switch {
		case len(offs) > 0:
			offs = schedule.NormalizeOffsets(offs)
		case !hasTime:
			offs = []int{0}
		default:
			offs = []int{s.RemindBefore}
		}

		items = append(items, importItem{
			Title: title, Notes: notes, At: r.At, Start: r.Start, Repeat: r.Repeat,
			Interval: r.Interval, Until: r.Until, Offsets: offs, NoTime: !hasTime,
		})
		if len(items) >= 150 {
			break
		}
	}
	return items, skippedPast, skippedBad
}

func newToken() string {
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func (b *Bot) offerImport(ctx context.Context, chatID int64, filename, caption, text string, res *extractResult) {
	s, loc := b.settings(chatID)
	now := time.Now().In(loc)
	items, past, bad := convertItems(res, s, loc, now)

	group := strings.TrimSpace(res.Group)
	if group == "" {
		group = strings.TrimSuffix(filename, "."+lastExt(filename))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "📎 <b>%s</b>\n", esc(filename))
	if res.Summary != "" {
		fmt.Fprintf(&sb, "%s\n", esc(res.Summary))
	}
	if len(items) == 0 {
		sb.WriteString("\nСобытий для напоминаний не осталось")
		if past > 0 {
			fmt.Fprintf(&sb, " (все %d — в прошлом)", past)
		}
		sb.WriteString(".")
		b.sendHTML(ctx, chatID, sb.String(), nil)
		b.appendHistory(chatID, fmt.Sprintf("Я прислал файл «%s». Начало содержимого:\n%s", filename, truncateRunes(text, maxFileCharsHist)),
			"В файле нет будущих событий для напоминаний.")
		return
	}

	fmt.Fprintf(&sb, "\nНашёл событий: <b>%d</b> → расписание «%s»\n\n", len(items), esc(group))
	for i, it := range items {
		if i >= 40 {
			fmt.Fprintf(&sb, "…и ещё %d\n", len(items)-i)
			break
		}
		at := it.At.In(loc)
		var when string
		switch it.Repeat {
		case storage.RepeatWeekly:
			when = wdShort[at.Weekday()]
			if it.Interval > 1 {
				when += fmt.Sprintf(" (раз в %d нед.)", it.Interval)
			}
		case storage.RepeatNone:
			when = fmt.Sprintf("%s %s", wdShort[at.Weekday()], at.Format("02.01"))
		default:
			r := storage.Reminder{Repeat: it.Repeat, Interval: it.Interval, Start: it.Start}
			when = repeatLabel(&r, loc)
		}
		clock := at.Format("15:04")
		if it.NoTime {
			clock = "весь день"
		}
		fmt.Fprintf(&sb, "• %s <b>%s</b> — %s", esc(when), clock, esc(it.Title))
		if it.Notes != "" {
			fmt.Fprintf(&sb, " <i>(%s)</i>", esc(truncateRunes(it.Notes, 80)))
		}
		sb.WriteString("\n")
	}
	if past > 0 {
		fmt.Fprintf(&sb, "\nПропущено прошедших: %d", past)
	}
	if bad > 0 {
		fmt.Fprintf(&sb, "\nНе удалось разобрать: %d", bad)
	}
	offs := items[0].Offsets
	for _, it := range items {
		if !it.NoTime {
			offs = it.Offsets
			break
		}
	}
	fmt.Fprintf(&sb, "\n🔔 Напоминания: %s (для событий без времени — в 9:00)\n\nДобавить?", offsetsLabel(offs))

	token := newToken()
	pi := &pendingImport{ChatID: chatID, Filename: filename, Group: group, Items: items, Created: time.Now()}
	b.pendMu.Lock()
	b.pending[token] = pi
	b.pendMu.Unlock()

	kb := telegram.Keyboard(telegram.Row(
		telegram.Button(fmt.Sprintf("✅ Добавить все (%d)", len(items)), "imp:ok:"+token),
		telegram.Button("❌ Отмена", "imp:no:"+token),
	))
	b.sendHTML(ctx, chatID, sb.String(), kb)

	userHist := fmt.Sprintf("Я прислал файл «%s».", filename)
	if caption != "" {
		userHist += " Комментарий: " + caption
	}
	userHist += "\nНачало содержимого:\n" + truncateRunes(text, maxFileCharsHist)
	b.appendHistory(chatID, userHist, fmt.Sprintf("Нашёл в файле %d событий для расписания «%s» и предложил добавить их кнопкой. %s",
		len(items), group, res.Summary))
}

func lastExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return ""
}

// handleImportCallback — кнопки «Добавить все» / «Отмена» под предпросмотром.
func (b *Bot) handleImportCallback(ctx context.Context, cq *telegram.CallbackQuery, action, token string) {
	chatID := cq.Message.Chat.ID
	b.pendMu.Lock()
	pi := b.pending[token]
	delete(b.pending, token)
	b.pendMu.Unlock()

	_ = b.tg.EditMessageReplyMarkup(ctx, chatID, cq.Message.MessageID, nil)
	if pi == nil || pi.ChatID != chatID {
		_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "Предпросмотр устарел — пришли файл ещё раз")
		return
	}
	if action != "ok" {
		_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "Отменено")
		b.sendPlain(ctx, chatID, "❌ Импорт отменён. Можешь прислать файл снова или сказать, что поправить.")
		return
	}

	added, dup := 0, 0
	_ = b.st.Write(func(d *storage.Data) {
		active := d.ActiveReminders(chatID)
		for _, it := range pi.Items {
			exists := false
			for _, ex := range active {
				if strings.EqualFold(ex.Text, it.Title) && ex.At.Equal(it.At) {
					exists = true
					break
				}
			}
			if exists {
				dup++
				continue
			}
			d.AddReminder(&storage.Reminder{
				ChatID: chatID, Text: it.Title, Notes: it.Notes, Group: pi.Group,
				Start: it.Start, At: it.At, Offsets: it.Offsets,
				Repeat: it.Repeat, Interval: it.Interval, Until: it.Until,
				CreatedAt: time.Now(), Source: "file:" + pi.Filename,
			})
			added++
		}
	})
	_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "Добавлено: "+strconv.Itoa(added))
	status := fmt.Sprintf("✅ <b>Добавлено напоминаний: %d</b> (расписание «%s»).", added, esc(pi.Group))
	if dup > 0 {
		status += fmt.Sprintf(" Уже были: %d.", dup)
	}
	status += "\nПосмотреть: /week · /list\nИзменить можно словами: «по расписанию " + esc(pi.Group) + " напоминай за 30 минут» или «удали расписание " + esc(pi.Group) + "»."
	b.sendHTML(ctx, chatID, status, nil)
	b.appendHistory(chatID, "[Нажал «Добавить все»]", fmt.Sprintf("Добавил %d напоминаний в расписание «%s».", added, pi.Group))
}

func (b *Bot) cleanupPending(now time.Time) {
	b.pendMu.Lock()
	defer b.pendMu.Unlock()
	for k, p := range b.pending {
		if now.Sub(p.Created) > 24*time.Hour {
			delete(b.pending, k)
		}
	}
}
