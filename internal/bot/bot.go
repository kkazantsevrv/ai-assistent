// Package bot — логика Telegram-ассистента: команды, диалог с ИИ,
// импорт расписаний из файлов и отправка напоминаний.
package bot

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/kkazantsevrv/ai-assistent/internal/config"
	"github.com/kkazantsevrv/ai-assistent/internal/llm"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
	"github.com/kkazantsevrv/ai-assistent/internal/telegram"
)

type Bot struct {
	cfg *config.Config
	st  *storage.Store
	tg  *telegram.Client
	ai  *llm.Client // nil, если ключ DeepSeek не задан

	chatLocks sync.Map // chatID -> *sync.Mutex

	queueMu sync.Mutex
	queues  map[int64]chan telegram.Update

	locMu sync.Mutex
	locs  map[string]*time.Location

	pendMu  sync.Mutex
	pending map[string]*pendingImport

	outMu  sync.Mutex
	outbox []outItem
}

func New(cfg *config.Config, st *storage.Store, tg *telegram.Client, ai *llm.Client) *Bot {
	return &Bot{
		cfg:     cfg,
		st:      st,
		tg:      tg,
		ai:      ai,
		locs:    map[string]*time.Location{},
		pending: map[string]*pendingImport{},
		queues:  map[int64]chan telegram.Update{},
	}
}

var botCommands = []telegram.BotCommand{
	{Command: "today", Description: "План на сегодня"},
	{Command: "tomorrow", Description: "План на завтра"},
	{Command: "week", Description: "Расписание на 7 дней"},
	{Command: "list", Description: "Все активные напоминания"},
	{Command: "remind", Description: "Быстрое напоминание: /remind 30m текст"},
	{Command: "notes", Description: "Заметки"},
	{Command: "note", Description: "Добавить заметку: /note текст"},
	{Command: "settings", Description: "Настройки: часовой пояс, время напоминаний"},
	{Command: "reset", Description: "Очистить историю диалога с ИИ"},
	{Command: "export", Description: "Выгрузить мои данные (JSON)"},
	{Command: "help", Description: "Что я умею"},
}

// Run запускает long polling и планировщик. Возвращается при отмене ctx.
func (b *Bot) Run(ctx context.Context) error {
	var me *telegram.User
	for {
		var err error
		me, err = b.tg.GetMe(ctx)
		if err == nil {
			break
		}
		if telegram.IsAPIError(err, 401) || telegram.IsAPIError(err, 404) {
			return errors.New("Telegram отклонил токен — проверьте TELEGRAM_BOT_TOKEN")
		}
		log.Printf("нет связи с Telegram: %v — повтор через 10 с", err)
		if !sleepCtx(ctx, 10*time.Second) {
			return ctx.Err()
		}
	}
	log.Printf("бот @%s запущен", me.Username)
	if b.ai == nil {
		log.Printf("DEEPSEEK_API_KEY не задан — работают только команды, без ИИ")
	}
	if len(b.cfg.AllowedUsers) == 0 {
		var owner int64
		b.st.Read(func(d *storage.Data) { owner = d.Owner })
		if owner == 0 {
			log.Printf("ALLOWED_USER_IDS не задан: первый, кто напишет боту, станет его владельцем")
		}
	}

	if err := b.tg.DeleteWebhook(ctx); err != nil {
		log.Printf("deleteWebhook: %v", err)
	}
	if err := b.tg.SetMyCommands(ctx, botCommands); err != nil {
		log.Printf("setMyCommands: %v", err)
	}

	go b.schedulerLoop(ctx)

	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		updates, err := b.tg.GetUpdates(ctx, offset, 50)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if telegram.IsAPIError(err, 409) {
				log.Printf("getUpdates: конфликт — запущена другая копия бота с этим токеном?")
			} else {
				log.Printf("getUpdates: %v", err)
			}
			sleepCtx(ctx, backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.dispatch(ctx, u)
		}
	}
	return ctx.Err()
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// dispatch отправляет сообщения в очередь своего чата, чтобы они
// обрабатывались строго по порядку; нажатия кнопок обрабатываются сразу.
func (b *Bot) dispatch(ctx context.Context, u telegram.Update) {
	if u.Message == nil {
		go b.handleUpdate(ctx, u)
		return
	}
	chatID := u.Message.Chat.ID
	b.queueMu.Lock()
	q, ok := b.queues[chatID]
	if !ok {
		q = make(chan telegram.Update, 64)
		b.queues[chatID] = q
		go func() {
			for {
				select {
				case u := <-q:
					b.handleUpdate(ctx, u)
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	b.queueMu.Unlock()
	select {
	case q <- u:
	default:
		go b.handleUpdate(ctx, u) // очередь переполнена — не блокируем приём
	}
}

func (b *Bot) handleUpdate(ctx context.Context, u telegram.Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic при обработке update %d: %v", u.UpdateID, r)
		}
	}()

	switch {
	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		if cq.Message == nil {
			return
		}
		if !b.authorized(&cq.From) {
			_ = b.tg.AnswerCallbackQuery(ctx, cq.ID, "Это личный бот")
			return
		}
		b.handleCallback(ctx, cq)

	case u.Message != nil:
		m := u.Message
		if m.From == nil || m.From.IsBot {
			return
		}
		if !b.authorized(m.From) {
			b.sendPlain(ctx, m.Chat.ID, "🔒 Это личный бот-ассистент.\nВаш Telegram ID: "+itoa(m.From.ID)+
				"\nЕсли это ваш бот — добавьте ID в ALLOWED_USER_IDS в файле .env.")
			log.Printf("отклонено сообщение от %d (@%s)", m.From.ID, m.From.Username)
			return
		}
		mu := b.chatLock(m.Chat.ID)
		mu.Lock()
		defer mu.Unlock()
		b.handleMessage(ctx, m)
	}
}

func (b *Bot) chatLock(chatID int64) *sync.Mutex {
	v, _ := b.chatLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// authorized проверяет доступ. Если список разрешённых пуст, первый
// написавший пользователь становится владельцем.
func (b *Bot) authorized(u *telegram.User) bool {
	if len(b.cfg.AllowedUsers) > 0 {
		return b.cfg.AllowedUsers[u.ID]
	}
	ok := false
	_ = b.st.WriteIf(func(d *storage.Data) bool {
		if d.Owner == 0 {
			d.Owner = u.ID
			ok = true
			log.Printf("владелец бота: %d (@%s)", u.ID, u.Username)
			return true
		}
		ok = d.Owner == u.ID
		return false
	})
	return ok
}

// ---------- настройки и часовой пояс ----------

func (b *Bot) defaultSettings() storage.Settings {
	return storage.Settings{
		Timezone:     b.cfg.Timezone,
		RemindBefore: b.cfg.DefaultRemindBefore,
		DigestTime:   b.cfg.DigestTime,
	}
}

func (b *Bot) location(name string) *time.Location {
	if name == "" {
		name = b.cfg.Timezone
	}
	b.locMu.Lock()
	defer b.locMu.Unlock()
	if l, ok := b.locs[name]; ok {
		return l
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		l, _ = time.LoadLocation(b.cfg.Timezone)
	}
	b.locs[name] = l
	return l
}

// settings возвращает настройки чата и его часовой пояс (чат создаётся при необходимости).
func (b *Bot) settings(chatID int64) (storage.Settings, *time.Location) {
	var s storage.Settings
	_ = b.st.WriteIf(func(d *storage.Data) bool {
		_, existed := d.Chats[chatID]
		s = d.Chat(chatID, b.defaultSettings(), time.Now().In(b.location(""))).Settings
		return !existed
	})
	return s, b.location(s.Timezone)
}

// locFor — часовой пояс чата внутри уже взятой блокировки хранилища.
func (b *Bot) locFor(d *storage.Data, chatID int64) *time.Location {
	if c, ok := d.Chats[chatID]; ok {
		return b.location(c.Settings.Timezone)
	}
	return b.location("")
}

// ---------- отправка ----------

func (b *Bot) sendPlain(ctx context.Context, chatID int64, text string) {
	for _, chunk := range splitMarkdown(text, 4000) {
		if _, err := b.tg.SendMessage(ctx, chatID, chunk, telegram.SendOptions{}); err != nil {
			log.Printf("sendMessage: %v", err)
		}
	}
}

// sendHTML отправляет заранее подготовленный HTML; клавиатура — к последней части.
func (b *Bot) sendHTML(ctx context.Context, chatID int64, html string, kb *telegram.InlineKeyboardMarkup) {
	chunks := splitLines(html, 4000)
	for i, chunk := range chunks {
		opt := telegram.SendOptions{ParseMode: "HTML"}
		if i == len(chunks)-1 {
			opt.ReplyMarkup = kb
		}
		if _, err := b.tg.SendMessage(ctx, chatID, chunk, opt); err != nil {
			log.Printf("sendMessage(HTML): %v", err)
			if telegram.IsAPIError(err, 400) {
				opt.ParseMode = ""
				_, _ = b.tg.SendMessage(ctx, chatID, stripTags(chunk), opt)
			}
		}
	}
}

// sendMarkdown отправляет ответ модели: Markdown → HTML, при ошибке разметки — простым текстом.
func (b *Bot) sendMarkdown(ctx context.Context, chatID int64, md string) {
	for _, chunk := range splitMarkdown(md, 3500) {
		html := markdownToHTML(chunk)
		if len([]rune(html)) <= 4096 {
			if _, err := b.tg.SendMessage(ctx, chatID, html, telegram.SendOptions{ParseMode: "HTML"}); err == nil {
				continue
			} else if !telegram.IsAPIError(err, 400) {
				log.Printf("sendMessage: %v", err)
				continue
			}
		}
		if _, err := b.tg.SendMessage(ctx, chatID, chunk, telegram.SendOptions{}); err != nil {
			log.Printf("sendMessage(plain): %v", err)
		}
	}
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>' && in:
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", "\"", "&amp;", "&").Replace(b.String())
}

// typing показывает «печатает…», пока не будет вызвана stop.
func (b *Bot) typing(ctx context.Context, chatID int64) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		for {
			_ = b.tg.SendChatAction(ctx, chatID, "typing")
			if !sleepCtx(ctx, 4500*time.Millisecond) {
				return
			}
		}
	}()
	return cancel
}
