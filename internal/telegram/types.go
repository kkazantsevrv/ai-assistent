package telegram

// Минимальный набор типов Telegram Bot API, нужный боту.

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type PhotoSize struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
}

type Message struct {
	MessageID      int64       `json:"message_id"`
	From           *User       `json:"from,omitempty"`
	Chat           Chat        `json:"chat"`
	Date           int64       `json:"date"`
	Text           string      `json:"text,omitempty"`
	Caption        string      `json:"caption,omitempty"`
	Document       *Document   `json:"document,omitempty"`
	Photo          []PhotoSize `json:"photo,omitempty"`
	Voice          *Voice      `json:"voice,omitempty"`
	Audio          *Document   `json:"audio,omitempty"`
	Video          *Document   `json:"video,omitempty"`
	Sticker        *struct{}   `json:"sticker,omitempty"`
	ReplyToMessage *Message    `json:"reply_to_message,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size,omitempty"`
	FilePath string `json:"file_path,omitempty"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// Row — удобный конструктор строки кнопок.
func Row(buttons ...InlineKeyboardButton) []InlineKeyboardButton { return buttons }

// Button — кнопка с callback-данными.
func Button(text, data string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text, CallbackData: data}
}

// Keyboard собирает разметку из строк кнопок.
func Keyboard(rows ...[]InlineKeyboardButton) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}
