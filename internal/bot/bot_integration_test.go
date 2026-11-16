package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/kkazantsevrv/ai-assistent/internal/config"
	"github.com/kkazantsevrv/ai-assistent/internal/llm"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
	"github.com/kkazantsevrv/ai-assistent/internal/telegram"
)

// fakeTelegram записывает вызовы Bot API и отдаёт файлы.
type fakeTelegram struct {
	mu    sync.Mutex
	calls []tgCall
	files map[string][]byte
}

type tgCall struct {
	Method string
	Params map[string]any
}

func (f *fakeTelegram) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/") {
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			w.Write(f.files[name])
			return
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var p map[string]any
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			json.NewDecoder(r.Body).Decode(&p)
		}
		f.mu.Lock()
		f.calls = append(f.calls, tgCall{method, p})
		f.mu.Unlock()

		var result any = true
		switch method {
		case "sendMessage":
			result = map[string]any{"message_id": 100, "chat": map[string]any{"id": p["chat_id"]}, "date": 0, "text": p["text"]}
		case "getFile":
			id := p["file_id"].(string)
			result = map[string]any{"file_id": id, "file_path": "documents/" + id}
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	})
}

func (f *fakeTelegram) sent(method string) []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTelegram) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// fakeDeepSeek: на обычный запрос вызывает add_reminder, после результата
// функции отвечает текстом; на JSON-запрос возвращает извлечённое расписание.
func fakeDeepSeek(t *testing.T, extraction string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.Request
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("некорректный запрос к DeepSeek: %v", err)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("нет ключа в заголовке")
		}
		var msg map[string]any
		last := req.Messages[len(req.Messages)-1]
		switch {
		case req.ResponseFormat != nil:
			msg = map[string]any{"role": "assistant", "content": extraction}
		case last.Role == "tool":
			if !strings.Contains(last.Content, `"ok":true`) {
				t.Errorf("функция вернула ошибку: %s", last.Content)
			}
			msg = map[string]any{"role": "assistant", "content": "**Готово!** Напомню через минуту."}
		default:
			if !strings.Contains(req.Messages[0].Content, "Календарь") {
				t.Errorf("в системном промпте нет календаря")
			}
			msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": "add_reminder", "arguments": `{"text":"Выпить воды","in_minutes":1,"remind_before_minutes":[0]}`},
			}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": msg, "finish_reason": "stop"}}})
	})
}

func newTestBot(t *testing.T, extraction string) (*Bot, *fakeTelegram, *storage.Store) {
	t.Helper()
	ftg := &fakeTelegram{files: map[string][]byte{}}
	tgSrv := httptest.NewServer(ftg.handler(t))
	t.Cleanup(tgSrv.Close)
	dsSrv := httptest.NewServer(fakeDeepSeek(t, extraction))
	t.Cleanup(dsSrv.Close)

	st, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Timezone: "Europe/Moscow", DefaultRemindBefore: 15, DigestTime: "",
		HistoryLimit: 30, DeepSeekTemperature: 0.7,
		AllowedUsers: map[int64]bool{42: true},
	}
	b := New(cfg, st, telegram.New("TOKEN", tgSrv.URL), llm.New("test-key", dsSrv.URL, "deepseek-chat"))
	return b, ftg, st
}

func textUpdate(text string) telegram.Update {
	return telegram.Update{UpdateID: 1, Message: &telegram.Message{
		MessageID: 1, From: &telegram.User{ID: 42, FirstName: "Я"}, Chat: telegram.Chat{ID: 42, Type: "private"}, Text: text,
	}}
}

func TestChatCreatesReminderAndNotifies(t *testing.T) {
	b, ftg, st := newTestBot(t, "")
	ctx := context.Background()

	b.handleUpdate(ctx, textUpdate("напомни через минуту выпить воды"))

	var r storage.Reminder
	st.Read(func(d *storage.Data) {
		if list := d.ActiveReminders(42); len(list) == 1 {
			r = *list[0]
		}
	})
	if r.Text != "Выпить воды" {
		t.Fatalf("напоминание не создано: %+v", r)
	}
	msgs := ftg.sent("sendMessage")
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Params["text"].(string), "<b>Готово!</b>") {
		t.Fatalf("нет ответа с HTML-разметкой: %+v", msgs)
	}
	var hist int
	st.Read(func(d *storage.Data) { hist = len(d.Chats[42].History) })
	if hist != 2 {
		t.Fatalf("история: %d сообщений", hist)
	}

	// Сдвигаем напоминание в прошлое — планировщик должен прислать уведомление.
	ftg.reset()
	st.Write(func(d *storage.Data) {
		x := d.Reminder(42, r.ID)
		x.At = time.Now().Add(-30 * time.Second)
		x.Start = x.At
	})
	b.tick(ctx)
	msgs = ftg.sent("sendMessage")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Params["text"].(string), "Выпить воды") {
		t.Fatalf("уведомление не отправлено: %+v", msgs)
	}
	kb, _ := json.Marshal(msgs[0].Params["reply_markup"])
	if !bytes.Contains(kb, []byte("done:")) || !bytes.Contains(kb, []byte("snz:")) {
		t.Fatalf("нет кнопок: %s", kb)
	}
	// Повторный тик — без дублей.
	b.tick(ctx)
	if n := len(ftg.sent("sendMessage")); n != 1 {
		t.Fatalf("дубль уведомления: %d", n)
	}

	// Кнопка «Отложить на 15 минут» создаёт новое напоминание.
	b.handleUpdate(ctx, telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID: "cb", From: telegram.User{ID: 42},
		Message: &telegram.Message{MessageID: 100, Chat: telegram.Chat{ID: 42}, Text: "🔔 Напоминание\n\nВыпить воды"},
		Data:    "snz:" + itoa(r.ID) + ":15",
	}})
	var active int
	st.Read(func(d *storage.Data) { active = len(d.ActiveReminders(42)) })
	if active != 1 {
		t.Fatalf("после «отложить» ожидалось 1 активное напоминание, есть %d", active)
	}
	if len(ftg.sent("editMessageText")) != 1 {
		t.Fatal("сообщение с кнопками не обновлено")
	}
}

func TestUnauthorizedUser(t *testing.T) {
	b, ftg, st := newTestBot(t, "")
	u := textUpdate("привет")
	u.Message.From.ID = 7
	b.handleUpdate(context.Background(), u)
	msgs := ftg.sent("sendMessage")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Params["text"].(string), "личный") {
		t.Fatalf("ожидался отказ: %+v", msgs)
	}
	st.Read(func(d *storage.Data) {
		if len(d.Chats) != 0 {
			t.Fatal("чужой пользователь не должен создавать состояние")
		}
	})
}

func TestCommandsWithoutAI(t *testing.T) {
	b, ftg, st := newTestBot(t, "")
	b.ai = nil
	ctx := context.Background()
	b.handleUpdate(ctx, textUpdate("/remind 2h проверить духовку"))
	b.handleUpdate(ctx, textUpdate("/note код домофона 1234"))
	b.handleUpdate(ctx, textUpdate("/list"))
	b.handleUpdate(ctx, textUpdate("/today"))
	b.handleUpdate(ctx, textUpdate("/settings"))

	var id int64
	st.Read(func(d *storage.Data) {
		if l := d.ActiveReminders(42); len(l) == 1 {
			id = l[0].ID
		}
		if len(d.ChatNotes(42)) != 1 {
			t.Error("заметка не сохранена")
		}
	})
	if id == 0 {
		t.Fatal("напоминание не создано")
	}
	for _, c := range ftg.sent("sendMessage") {
		if c.Params["parse_mode"] == "HTML" && strings.Contains(c.Params["text"].(string), "Активные напоминания") {
			if !strings.Contains(c.Params["text"].(string), "/del_"+itoa(id)) {
				t.Errorf("в /list нет быстрой команды удаления")
			}
		}
	}
	b.handleUpdate(ctx, textUpdate("/del_"+itoa(id)))
	st.Read(func(d *storage.Data) {
		if len(d.ActiveReminders(42)) != 0 {
			t.Error("/del не удалил напоминание")
		}
	})
}

func TestImportScheduleFromXLSX(t *testing.T) {
	extraction := `{"is_schedule":true,"summary":"Расписание пар","group":"Пары","items":[
		{"title":"Математика (лекция)","weekday":"mon","time":"09:00","end_time":"10:30","repeat":"weekly","notes":"ауд. 305","remind_before_minutes":[]},
		{"title":"Физика","weekday":"wed","time":"11:00","repeat":"weekly","remind_before_minutes":[10]},
		{"title":"Сдать курсовую","date":"2099-12-20","time":"","repeat":"none"},
		{"title":"Прошедший зачёт","date":"2020-01-10","time":"10:00","repeat":"none"}
	]}`
	b, ftg, st := newTestBot(t, extraction)
	ctx := context.Background()

	f := excelize.NewFile()
	f.SetCellValue("Sheet1", "A1", "Пн")
	f.SetCellValue("Sheet1", "B1", "09:00 Математика")
	var buf bytes.Buffer
	f.Write(&buf)
	ftg.files["FILE1"] = buf.Bytes()

	b.handleUpdate(ctx, telegram.Update{Message: &telegram.Message{
		MessageID: 5, From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: 42},
		Document: &telegram.Document{FileID: "FILE1", FileName: "пары.xlsx", FileSize: int64(buf.Len())},
	}})

	msgs := ftg.sent("sendMessage")
	if len(msgs) == 0 {
		t.Fatal("нет предпросмотра")
	}
	preview := msgs[len(msgs)-1]
	text := preview.Params["text"].(string)
	if !strings.Contains(text, "Нашёл событий: <b>3</b>") || !strings.Contains(text, "Пропущено прошедших: 1") {
		t.Fatalf("неожиданный предпросмотр:\n%s", text)
	}
	kb, _ := json.Marshal(preview.Params["reply_markup"])
	token := ""
	if i := bytes.Index(kb, []byte("imp:ok:")); i >= 0 {
		token = string(kb[i+7 : i+19])
	}
	if token == "" {
		t.Fatalf("нет кнопки импорта: %s", kb)
	}

	b.handleUpdate(ctx, telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID: "cb", From: telegram.User{ID: 42},
		Message: &telegram.Message{MessageID: 100, Chat: telegram.Chat{ID: 42}},
		Data:    "imp:ok:" + token,
	}})

	st.Read(func(d *storage.Data) {
		list := d.ActiveReminders(42)
		if len(list) != 3 {
			t.Fatalf("ожидалось 3 напоминания, есть %d", len(list))
		}
		loc, _ := time.LoadLocation("Europe/Moscow")
		for _, r := range list {
			if r.Group != "Пары" {
				t.Errorf("%s: группа %q", r.Text, r.Group)
			}
			switch r.Text {
			case "Математика (лекция)":
				at := r.At.In(loc)
				if at.Weekday() != time.Monday || at.Format("15:04") != "09:00" || r.Repeat != storage.RepeatWeekly {
					t.Errorf("математика: %v %s", at, r.Repeat)
				}
				if r.Offsets[0] != 15 || !strings.Contains(r.Notes, "до 10:30") {
					t.Errorf("математика: offsets=%v notes=%q", r.Offsets, r.Notes)
				}
			case "Физика":
				if r.Offsets[0] != 10 {
					t.Errorf("физика: offsets=%v", r.Offsets)
				}
			case "Сдать курсовую":
				if r.At.In(loc).Format("15:04") != "09:00" || r.Offsets[0] != 0 {
					t.Errorf("курсовая: %v %v", r.At.In(loc), r.Offsets)
				}
			}
		}
	})
}
