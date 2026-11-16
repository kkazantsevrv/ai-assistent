// Личный ИИ-ассистент в Telegram: напоминания, расписание, заметки и свободный
// диалог на базе DeepSeek. Работает на ПК, сервере и в Termux на Android.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "time/tzdata" // встроенная база часовых поясов (на Android своей может не быть)

	"github.com/kkazantsevrv/ai-assistent/internal/bot"
	"github.com/kkazantsevrv/ai-assistent/internal/config"
	"github.com/kkazantsevrv/ai-assistent/internal/llm"
	"github.com/kkazantsevrv/ai-assistent/internal/netfix"
	"github.com/kkazantsevrv/ai-assistent/internal/storage"
	"github.com/kkazantsevrv/ai-assistent/internal/telegram"
)

var version = "dev"

func main() {
	envFile := flag.String("env", ".env", "путь к файлу настроек")
	showVersion := flag.Bool("version", false, "показать версию")
	flag.Parse()

	if *showVersion {
		fmt.Println("ai-assistant", version)
		return
	}

	log.SetFlags(log.Ldate | log.Ltime)

	cfg, err := config.Load(*envFile)
	if err != nil {
		log.Fatalf("настройки: %v", err)
	}

	netfix.Apply(cfg.DNSServers)

	st, err := storage.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("хранилище: %v", err)
	}

	var ai *llm.Client
	if cfg.DeepSeekKey != "" {
		ai = llm.New(cfg.DeepSeekKey, cfg.DeepSeekBaseURL, cfg.DeepSeekModel)
	}

	b := bot.New(cfg, st, telegram.New(cfg.TelegramToken, cfg.TelegramAPIURL), ai)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := b.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("ошибка: %v", err)
	}
	log.Printf("остановлен")
}
