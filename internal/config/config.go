// Package config загружает настройки бота из .env-файла и переменных окружения.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — все настройки приложения.
type Config struct {
	TelegramToken  string
	TelegramAPIURL string

	DeepSeekKey         string
	DeepSeekBaseURL     string
	DeepSeekModel       string
	DeepSeekTemperature float64

	AllowedUsers map[int64]bool

	Timezone            string
	DefaultRemindBefore int    // минут до события по умолчанию
	DigestTime          string // "08:00" или "" (выключено)

	DataDir      string
	HistoryLimit int

	DNSServers []string
}

// Load читает .env (если файл есть) и переменные окружения.
// Переменные окружения имеют приоритет над значениями из файла.
func Load(envFile string) (*Config, error) {
	if envFile != "" {
		if err := loadDotEnv(envFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("чтение %s: %w", envFile, err)
		}
	}

	c := &Config{
		TelegramToken:       env("TELEGRAM_BOT_TOKEN", ""),
		TelegramAPIURL:      strings.TrimRight(env("TELEGRAM_API_URL", "https://api.telegram.org"), "/"),
		DeepSeekKey:         env("DEEPSEEK_API_KEY", ""),
		DeepSeekBaseURL:     strings.TrimRight(env("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), "/"),
		DeepSeekModel:       env("DEEPSEEK_MODEL", "deepseek-chat"),
		DeepSeekTemperature: envFloat("DEEPSEEK_TEMPERATURE", 0.7),
		AllowedUsers:        map[int64]bool{},
		Timezone:            env("TIMEZONE", "Europe/Moscow"),
		DefaultRemindBefore: envInt("DEFAULT_REMIND_BEFORE", 15),
		DigestTime:          env("DAILY_DIGEST_TIME", "08:00"),
		DataDir:             env("DATA_DIR", "./data"),
		HistoryLimit:        envInt("HISTORY_LIMIT", 30),
	}

	if c.TelegramToken == "" {
		return nil, errors.New("не задан TELEGRAM_BOT_TOKEN (см. .env.example)")
	}

	for _, part := range splitList(env("ALLOWED_USER_IDS", "")) {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ALLOWED_USER_IDS: неверный id %q", part)
		}
		c.AllowedUsers[id] = true
	}

	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return nil, fmt.Errorf("TIMEZONE: неизвестный часовой пояс %q", c.Timezone)
	}

	switch strings.ToLower(c.DigestTime) {
	case "", "off", "no", "false", "0":
		c.DigestTime = ""
	default:
		if _, err := time.Parse("15:04", c.DigestTime); err != nil {
			return nil, fmt.Errorf("DAILY_DIGEST_TIME: ожидается ЧЧ:ММ или off, получено %q", c.DigestTime)
		}
	}

	if c.DefaultRemindBefore < 0 {
		c.DefaultRemindBefore = 0
	}
	if c.HistoryLimit < 2 {
		c.HistoryLimit = 2
	}

	for _, s := range splitList(env("DNS_SERVERS", "")) {
		if !strings.Contains(s, ":") {
			s += ":53"
		}
		c.DNSServers = append(c.DNSServers, s)
	}

	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) int {
	v := env(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v := env(key, "")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
	if err != nil {
		return def
	}
	return f
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadDotEnv — минимальный парсер .env: KEY=VALUE, комментарии #, кавычки.
// Уже заданные переменные окружения не перезаписываются.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
	return sc.Err()
}
