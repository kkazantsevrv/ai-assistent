package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/llm"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
)

const (
	maxToolRounds   = 8
	historyMaxChars = 24000
)

// chat — один ход диалога: отправляет сообщение модели (с инструментами),
// сохраняет историю и отвечает пользователю. historyText — как сохранить
// реплику пользователя в истории (например, без полного текста файла).
func (b *Bot) chat(ctx context.Context, chatID int64, userText, historyText string) {
	if b.ai == nil {
		b.sendPlain(ctx, chatID, "🤖 ИИ не подключён (не задан DEEPSEEK_API_KEY). Доступны команды — см. /help.\n"+
			"Быстрое напоминание: /remind 30m текст")
		return
	}
	stop := b.typing(ctx, chatID)
	reply, err := b.converse(ctx, chatID, userText)
	stop()
	if err != nil {
		log.Printf("deepseek: %v", err)
		b.sendPlain(ctx, chatID, "⚠️ Не получилось получить ответ ИИ: "+err.Error()+
			"\nПопробуй ещё раз чуть позже. Команды (/list, /remind, /today) работают и без ИИ.")
		return
	}
	b.appendHistory(chatID, historyText, reply)
	b.sendMarkdown(ctx, chatID, reply)
}

func (b *Bot) converse(ctx context.Context, chatID int64, userText string) (string, error) {
	settings, loc := b.settings(chatID)
	now := time.Now().In(loc)

	msgs := []llm.Message{{Role: "system", Content: b.systemPrompt(chatID, settings, loc, now)}}
	b.st.Read(func(d *storage.Data) {
		if c, ok := d.Chats[chatID]; ok {
			for _, h := range c.History {
				msgs = append(msgs, llm.Message{Role: h.Role, Content: h.Content})
			}
		}
	})
	msgs = append(msgs, llm.Message{Role: "user", Content: userText})

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	temp := llm.Float(b.cfg.DeepSeekTemperature)

	for round := 0; round < maxToolRounds; round++ {
		resp, _, err := b.ai.Chat(ctx, llm.Request{Messages: msgs, Tools: toolDefs, Temperature: temp, MaxTokens: 4096})
		if err != nil {
			return "", err
		}
		if len(resp.ToolCalls) == 0 {
			if text := strings.TrimSpace(resp.Content); text != "" {
				return text, nil
			}
			return "Готово.", nil
		}
		msgs = append(msgs, llm.Message{
			Role:             "assistant",
			Content:          resp.Content,
			ReasoningContent: resp.ReasoningContent,
			ToolCalls:        resp.ToolCalls,
		})
		for _, tc := range resp.ToolCalls {
			msgs = append(msgs, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    b.execTool(chatID, tc.Function.Name, tc.Function.Arguments),
			})
		}
	}

	// Слишком много раундов — просим модель подвести итог без инструментов.
	resp, _, err := b.ai.Chat(ctx, llm.Request{Messages: msgs, Tools: toolDefs, ToolChoice: "none", Temperature: temp, MaxTokens: 2048})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

// appendHistory сохраняет пару реплик и обрезает историю по количеству и объёму.
func (b *Bot) appendHistory(chatID int64, user, assistant string) {
	_ = b.st.Write(func(d *storage.Data) {
		c := d.Chat(chatID, b.defaultSettings(), time.Now())
		now := time.Now()
		c.History = append(c.History,
			storage.ChatMessage{Role: "user", Content: user, Time: now},
			storage.ChatMessage{Role: "assistant", Content: assistant, Time: now},
		)
		total := 0
		for _, m := range c.History {
			total += len([]rune(m.Content))
		}
		for len(c.History) > 2 && (len(c.History) > b.cfg.HistoryLimit || total > historyMaxChars) {
			total -= len([]rune(c.History[0].Content))
			c.History = c.History[1:]
		}
		// История должна начинаться с реплики пользователя.
		for len(c.History) > 0 && c.History[0].Role != "user" {
			c.History = c.History[1:]
		}
	})
}

func (b *Bot) systemPrompt(chatID int64, s storage.Settings, loc *time.Location, now time.Time) string {
	var sb strings.Builder
	sb.WriteString(`Ты — личный ИИ-ассистент и секретарь пользователя в Telegram. Главная задача — помогать с делами: напоминания, расписание, заметки, планирование дня и недели. При этом ты полноценный собеседник: можешь обсуждать любые темы, отвечать на вопросы, советовать, помогать с текстами, учёбой, работой, кодом, идеями. Не отказывайся от обычного разговора и не своди всё к напоминаниям.

`)
	_, offset := now.Zone()
	fmt.Fprintf(&sb, "## Текущий момент\nСейчас: %s, %d %s %d, %s. Часовой пояс пользователя: %s (UTC%s).\n",
		wdFull[now.Weekday()], now.Day(), monthGen[now.Month()], now.Year(), now.Format("15:04"), s.Timezone, utcOffset(offset))
	sb.WriteString("Календарь на две недели:\n")
	for i := 0; i < 14; i++ {
		d := now.AddDate(0, 0, i)
		mark := ""
		switch i {
		case 0:
			mark = " — сегодня"
		case 1:
			mark = " — завтра"
		case 2:
			mark = " — послезавтра"
		}
		fmt.Fprintf(&sb, "%s %s%s\n", wdShort[d.Weekday()], d.Format("2006-01-02"), mark)
	}

	digest := "выключена"
	if s.DigestTime != "" {
		digest = "в " + s.DigestTime
	}
	fmt.Fprintf(&sb, "\n## Настройки\n- О событиях по умолчанию напоминать: %s (remind_before_minutes [%d])\n- Утренняя сводка: %s\n",
		offsetLabel(s.RemindBefore), s.RemindBefore, digest)

	sb.WriteString("\n## Ближайшие активные напоминания\n")
	b.st.Read(func(d *storage.Data) {
		list := d.ActiveReminders(chatID)
		if len(list) == 0 {
			sb.WriteString("(нет)\n")
		}
		for i, r := range list {
			if i >= 25 {
				fmt.Fprintf(&sb, "…всего активных: %d (остальные — через list_reminders)\n", len(list))
				break
			}
			fmt.Fprintf(&sb, "#%d | %s %s | %s | уведомления: %s", r.ID, wdShort[r.At.In(loc).Weekday()],
				r.At.In(loc).Format("2006-01-02 15:04"), r.Text, offsetsLabel(r.EffectiveOffsets()))
			if rl := repeatLabel(r, loc); rl != "" {
				sb.WriteString(" | повтор: " + rl)
			}
			if r.Group != "" {
				sb.WriteString(" | группа: " + r.Group)
			}
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "Сохранённых заметок: %d (искать — list_notes).\n", len(d.ChatNotes(chatID)))
	})

	fmt.Fprintf(&sb, `
## Работа с напоминаниями
- Любые действия с напоминаниями, расписанием, заметками и настройками выполняй ТОЛЬКО через функции. Никогда не пиши «напомню», «записал», «удалил», если не вызвал функцию и не получил "ok": true.
- Время передавай в часовом поясе пользователя: YYYY-MM-DDTHH:MM. Для «через N минут/часов» используй in_minutes.
- remind_before_minutes:
  • «напомни в 9:00 …», «через час …» — простое напоминание → [0];
  • событие (встреча, пара, приём у врача, тренировка, поезд, дедлайн) → [%d] по умолчанию, для важных можно [%d, 0];
  • если пользователь сказал «за час», «за день и за час» — ровно так: [60], [1440, 60].
- Если время не указано: «утром» — 09:00, «днём» — 13:00, «вечером» — 19:00, дата без времени — 09:00 (и скажи об этом). Если непонятно даже примерно — уточни.
- Повторы: daily, weekly, weekdays (по будням), monthly, yearly; interval — каждые N; until — до какой даты. «По понедельникам и средам» = отдельное weekly-напоминание на каждый день.
- Расписание (набор регулярных событий) создавай несколькими add_reminder с общим group («Пары», «Тренировки», «Смены»).
- Чтобы изменить или удалить, найди id в списке выше или через list_reminders, затем update_reminder / delete_reminders. Массовое удаление (больше 3 штук) делай, только если пользователь явно попросил.
- На вопросы «что у меня завтра / на неделе / в пятницу» вызывай get_schedule — он раскрывает повторы.
- Если пользователь просит запомнить информацию (пароль от Wi-Fi, размер обуви, идею, список) — add_note. Вопросы «что я записывал про…» — list_notes.
- После действия кратко подтверди человеческим языком: что, когда и за сколько напомнишь (бери дату из ответа функции). Номер (#id) указывай коротко.

## Стиль
- Отвечай на языке пользователя (обычно русский), дружелюбно и по делу; эмодзи — умеренно.
- Оформление — простой Markdown: **жирный**, списки, `+"`код`"+`. Тебя читают в Telegram на телефоне: без лишней «воды», таблицы — только если действительно нужны.
`, s.RemindBefore, s.RemindBefore)
	return sb.String()
}

func utcOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign = "-"
		sec = -sec
	}
	return fmt.Sprintf("%s%02d:%02d", sign, sec/3600, sec%3600/60)
}
