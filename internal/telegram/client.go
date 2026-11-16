// Package telegram — небольшой клиент Telegram Bot API без внешних зависимостей.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIError — ошибка, которую вернул Telegram.
type APIError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %d %s", e.Code, e.Description)
}

// IsAPIError сообщает, является ли err ошибкой Telegram с данным кодом.
func IsAPIError(err error, code int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == code
}

type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

func New(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	return &Client{
		token:   token,
		baseURL: baseURL,
		// Таймаут больше, чем long polling (50 c), с запасом на медленную сеть.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) methodURL(method string) string {
	return c.baseURL + "/bot" + c.token + "/" + method
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		err = c.do(req, out)
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == 429 && ae.RetryAfter > 0 && attempt < 3 {
			select {
			case <-time.After(time.Duration(ae.RetryAfter) * time.Second):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return redact(err, c.token)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	var ar apiResponse
	if err := json.Unmarshal(data, &ar); err != nil {
		return fmt.Errorf("telegram: неожиданный ответ (HTTP %d): %.200s", resp.StatusCode, data)
	}
	if !ar.OK {
		ae := &APIError{Code: ar.ErrorCode, Description: ar.Description}
		if ar.Parameters != nil {
			ae.RetryAfter = ar.Parameters.RetryAfter
		}
		return ae
	}
	if out != nil {
		return json.Unmarshal(ar.Result, out)
	}
	return nil
}

// redact убирает токен из текста сетевых ошибок (в них попадает URL).
func redact(err error, token string) error {
	msg := err.Error()
	if token == "" || !strings.Contains(msg, token) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, token, "<TOKEN>"))
}

func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	return &u, c.call(ctx, "getMe", struct{}{}, &u)
}

func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// SendOptions — необязательные параметры отправки сообщения.
type SendOptions struct {
	ParseMode   string // "HTML" или ""
	ReplyMarkup *InlineKeyboardMarkup
	ReplyTo     int64
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, opt SendOptions) (*Message, error) {
	p := map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if opt.ParseMode != "" {
		p["parse_mode"] = opt.ParseMode
	}
	if opt.ReplyMarkup != nil {
		p["reply_markup"] = opt.ReplyMarkup
	}
	if opt.ReplyTo != 0 {
		p["reply_parameters"] = map[string]any{"message_id": opt.ReplyTo, "allow_sending_without_reply": true}
	}
	var m Message
	return &m, c.call(ctx, "sendMessage", p, &m)
}

// EditMessageText меняет текст сообщения. Если markup == nil, клавиатура убирается.
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *InlineKeyboardMarkup) error {
	p := map[string]any{
		"chat_id":              chatID,
		"message_id":           messageID,
		"text":                 text,
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if parseMode != "" {
		p["parse_mode"] = parseMode
	}
	if markup != nil {
		p["reply_markup"] = markup
	}
	err := c.call(ctx, "editMessageText", p, nil)
	if isNotModified(err) {
		return nil
	}
	return err
}

func isNotModified(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified")
}

func (c *Client) EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *InlineKeyboardMarkup) error {
	p := map[string]any{"chat_id": chatID, "message_id": messageID}
	if markup != nil {
		p["reply_markup"] = markup
	} else {
		p["reply_markup"] = InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}
	}
	err := c.call(ctx, "editMessageReplyMarkup", p, nil)
	if isNotModified(err) {
		return nil
	}
	return err
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	p := map[string]any{"callback_query_id": id}
	if text != "" {
		p["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", p, nil)
}

func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}

func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}

func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	var f File
	return &f, c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f)
}

// DownloadFile скачивает файл по file_path, полученному из GetFile.
func (c *Client) DownloadFile(ctx context.Context, filePath string, maxBytes int64) ([]byte, error) {
	url := c.baseURL + "/file/bot" + c.token + "/" + filePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, redact(err, c.token)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram: скачивание файла: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("файл больше %d МБ", maxBytes>>20)
	}
	return data, nil
}

// SendDocument отправляет файл из памяти.
func (c *Client) SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	fw, err := w.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL("sendDocument"), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return c.do(req, nil)
}
