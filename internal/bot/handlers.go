package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/schedule"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
	"github.com/kkazantsevrv/ai-assistent/internal/telegram"
)

func (b *Bot) handleMessage(ctx context.Context, m *telegram.Message) {
	chatID := m.Chat.ID

	switch {
	case m.Document != nil:
		b.handleDocument(ctx, m)
		return
	case len(m.Photo) > 0 && strings.TrimSpace(m.Caption) == "":
		b.sendPlain(ctx, chatID, "📷 Картинки пока не умею распознавать. Пришли текстом или файлом (.txt, .docx, .xlsx).")
		return
	case m.Voice != nil || m.Audio != nil || m.Video != nil:
		b.sendPlain(ctx, chatID, "🎤 Голосовые и видео пока не поддерживаю — напиши текстом, пожалуйста.")
		return
	case m.Sticker != nil:
		b.sendPlain(ctx, chatID, "🙂")
		return
	}

	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/") && b.handleCommand(ctx, m, text) {
		return
	}

	// Ответ на сообщение бота (например, на напоминание) — добавляем контекст.
	userText := text
	if rt := m.ReplyToMessage; rt != nil {
		quoted := strings.TrimSpace(rt.Text + rt.Caption)
		if quoted != "" {
			userText = fmt.Sprintf("[В ответ на сообщение: «%s»]\n%s", truncateRunes(quoted, 1000), text)
		}
	}
	b.chat(ctx, chatID, userText, userText)
}

// handleCallback — нажатия на кнопки под сообщениями.
func (b *Bot) handleCallback(ctx context.Context, cq *telegram.CallbackQuery) {
	chatID := cq.Message.Chat.ID
	parts := strings.Split(cq.Data, ":")
	if len(parts) < 2 {
		_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "")
		return
	}

	switch parts[0] {
	case "imp":
		if len(parts) == 3 {
			b.handleImportCallback(ctx, cq, parts[1], parts[2])
			return
		}

	case "done":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		var eventAt time.Time
		if len(parts) > 2 {
			if u, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
				eventAt = time.Unix(u, 0)
			}
		}
		_, loc := b.settings(chatID)
		status := "✅ Выполнено"
		_ = b.st.WriteIf(func(d *storage.Data) bool {
			r := d.Reminder(chatID, id)
			if r == nil {
				return false
			}
			changed := schedule.CompleteOccurrence(r, eventAt, time.Now(), loc)
			if !r.Done {
				status += " · следующее: " + whenLabel(r.At.In(loc), time.Now().In(loc))
			}
			return changed
		})
		_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "Отмечено ✅")
		b.appendStatus(ctx, cq.Message, status)
		return

	case "snz":
		if len(parts) != 3 {
			break
		}
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		mins, err := strconv.Atoi(parts[2])
		if err != nil || mins <= 0 {
			break
		}
		_, loc := b.settings(chatID)
		at := time.Now().Add(time.Duration(mins) * time.Minute).Truncate(time.Minute)
		found := false
		_ = b.st.WriteIf(func(d *storage.Data) bool {
			src := d.Reminder(chatID, id)
			if src == nil {
				return false
			}
			found = true
			d.AddReminder(&storage.Reminder{
				ChatID: chatID, Text: src.Text, Notes: src.Notes,
				Start: at, At: at, Offsets: []int{0},
				CreatedAt: time.Now(), Source: "snooze",
			})
			return true
		})
		if !found {
			_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "Напоминание уже удалено")
			return
		}
		label := whenLabel(at.In(loc), time.Now().In(loc))
		_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "Напомню "+label)
		b.appendStatus(ctx, cq.Message, "⏰ Отложено: напомню "+label)
		return
	}
	_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "")
}

// appendStatus дописывает строку статуса к сообщению и убирает кнопки.
func (b *Bot) appendStatus(ctx context.Context, m *telegram.Message, status string) {
	text := m.Text
	if text == "" {
		_ = b.tg.EditMessageReplyMarkup(ctx, m.Chat.ID, m.MessageID, nil)
		return
	}
	if err := b.tg.EditMessageText(ctx, m.Chat.ID, m.MessageID, text+"\n\n"+status, "", nil); err != nil {
		_ = b.tg.EditMessageReplyMarkup(ctx, m.Chat.ID, m.MessageID, nil)
	}
}
