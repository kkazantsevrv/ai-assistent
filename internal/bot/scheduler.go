package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/schedule"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
	"github.com/kkazantsevrv/ai-assistent/internal/telegram"
)

const (
	tickInterval  = 20 * time.Second
	outboxMaxAge  = 12 * time.Hour
	digestLateMax = 3 * time.Hour
)

// outItem — сообщение в очереди на отправку. Если сети нет (телефон
// без интернета), уведомление не теряется, а уходит, когда связь вернётся.
type outItem struct {
	chatID  int64
	html    string
	kb      *telegram.InlineKeyboardMarkup
	created time.Time
}

func (b *Bot) schedulerLoop(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	b.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.tick(ctx)
		}
	}
}

func (b *Bot) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic в планировщике: %v", r)
		}
	}()
	now := time.Now()
	var queue []outItem

	err := b.st.WriteIf(func(d *storage.Data) bool {
		changed := false
		for _, r := range d.Reminders {
			if r.Done {
				continue
			}
			loc := b.locFor(d, r.ChatID)
			n, ch := schedule.Process(r, now, loc)
			changed = changed || ch
			if n != nil {
				queue = append(queue, outItem{
					chatID:  r.ChatID,
					html:    notificationHTML(n, now.In(loc), loc),
					kb:      notificationKeyboard(n),
					created: now,
				})
			}
		}
		if d.CleanupDone(now, 30*24*time.Hour) {
			changed = true
		}
		for chatID, c := range d.Chats {
			if html, sent := b.digestDue(d, chatID, c, now); sent {
				changed = true
				if html != "" {
					queue = append(queue, outItem{chatID: chatID, html: html, created: now})
				}
			}
		}
		return changed
	})
	if err != nil {
		log.Printf("сохранение данных: %v", err)
	}

	b.outMu.Lock()
	b.outbox = append(b.outbox, queue...)
	b.outMu.Unlock()
	b.flushOutbox(ctx)
	b.cleanupPending(now)
}

func (b *Bot) flushOutbox(ctx context.Context) {
	b.outMu.Lock()
	items := b.outbox
	b.outbox = nil
	b.outMu.Unlock()
	if len(items) == 0 {
		return
	}

	var keep []outItem
	for i, it := range items {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := b.tg.SendMessage(sctx, it.chatID, it.html, telegram.SendOptions{ParseMode: "HTML", ReplyMarkup: it.kb})
		if err != nil && telegram.IsAPIError(err, 400) {
			// Проблема с разметкой — отправим простым текстом.
			_, err = b.tg.SendMessage(sctx, it.chatID, stripTags(it.html), telegram.SendOptions{ReplyMarkup: it.kb})
		}
		cancel()
		if err == nil {
			continue
		}
		var ae *telegram.APIError
		if errors.As(err, &ae) {
			// Telegram ответил отказом (бот заблокирован, чат не найден) — не повторяем.
			log.Printf("уведомление в чат %d отброшено: %v", it.chatID, err)
			continue
		}
		log.Printf("нет связи, уведомление отложено: %v", err)
		for _, rest := range items[i:] {
			if time.Since(rest.created) < outboxMaxAge {
				keep = append(keep, rest)
			}
		}
		break
	}
	if len(keep) > 0 {
		b.outMu.Lock()
		b.outbox = append(keep, b.outbox...)
		b.outMu.Unlock()
	}
}

func notificationHTML(n *schedule.Notification, now time.Time, loc *time.Location) string {
	r := n.Reminder
	ev := n.EventAt.In(loc)
	var sb strings.Builder
	switch {
	case n.Missed:
		fmt.Fprintf(&sb, "⚠️ <b>Пропущенное напоминание</b> (%s)\n\n", esc(whenLabel(ev, now)))
	case n.Offset > 0 && now.Before(ev):
		fmt.Fprintf(&sb, "🔔 <b>Через %s</b> · %s\n\n", esc(humanDuration(ev.Sub(now))), esc(whenLabel(ev, now)))
	default:
		fmt.Fprintf(&sb, "🔔 <b>Напоминание</b> · %s\n\n", ev.Format("15:04"))
	}
	fmt.Fprintf(&sb, "<b>%s</b>", esc(r.Text))
	if r.Notes != "" {
		fmt.Fprintf(&sb, "\n📝 %s", esc(r.Notes))
	}
	if rl := repeatLabel(&r, loc); rl != "" {
		fmt.Fprintf(&sb, "\n🔁 %s", esc(rl))
	}
	fmt.Fprintf(&sb, "\n<i>#%d</i>", r.ID)
	return sb.String()
}

func notificationKeyboard(n *schedule.Notification) *telegram.InlineKeyboardMarkup {
	id := n.Reminder.ID
	return telegram.Keyboard(
		telegram.Row(telegram.Button("✅ Готово", fmt.Sprintf("done:%d:%d", id, n.EventAt.Unix()))),
		telegram.Row(
			telegram.Button("⏰ 15 мин", fmt.Sprintf("snz:%d:15", id)),
			telegram.Button("⏰ 1 час", fmt.Sprintf("snz:%d:60", id)),
			telegram.Button("⏰ Завтра", fmt.Sprintf("snz:%d:1440", id)),
		),
	)
}

// digestDue проверяет, пора ли отправить утреннюю сводку. Возвращает HTML
// (пустой, если событий нет) и признак того, что сводка за сегодня отмечена.
func (b *Bot) digestDue(d *storage.Data, chatID int64, c *storage.ChatState, now time.Time) (string, bool) {
	if c.Settings.DigestTime == "" {
		return "", false
	}
	loc := b.location(c.Settings.Timezone)
	lnow := now.In(loc)
	today := lnow.Format("2006-01-02")
	if c.LastDigest == today {
		return "", false
	}
	h, m, ok := parseClock(c.Settings.DigestTime)
	if !ok {
		return "", false
	}
	target := time.Date(lnow.Year(), lnow.Month(), lnow.Day(), h, m, 0, 0, loc)
	if lnow.Before(target) {
		return "", false
	}
	c.LastDigest = today
	if lnow.Sub(target) > digestLateMax {
		return "", true // проспали — поздно присылать «доброе утро»
	}
	occ := b.agenda(d, chatID, dayStart(lnow), dayStart(lnow).AddDate(0, 0, 1), loc)
	if len(occ) == 0 {
		return "", true
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "☀️ <b>Доброе утро! План на %s:</b>\n\n", esc(dayLabel(lnow, lnow)))
	for _, o := range occ {
		fmt.Fprintf(&sb, "• <b>%s</b> %s", o.At.Format("15:04"), esc(o.Text))
		if o.Notes != "" {
			fmt.Fprintf(&sb, " — <i>%s</i>", esc(truncateRunes(o.Notes, 100)))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nХорошего дня! Напомню о каждом деле заранее.")
	return sb.String(), true
}
