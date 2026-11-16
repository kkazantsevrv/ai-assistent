#!/usr/bin/env bash
# Запуск бота в фоне (работает и после закрытия окна Termux).
cd "$(dirname "$0")/.."

if pgrep -f "scripts/run.sh" >/dev/null 2>&1; then
  echo "Бот уже запущен. Журнал: bash scripts/logs.sh"
  exit 0
fi

mkdir -p data
nohup bash scripts/run.sh >/dev/null 2>&1 &
sleep 2
echo "Бот запущен в фоне."
echo "Журнал:     bash scripts/logs.sh"
echo "Остановить: bash scripts/stop.sh"
tail -n 5 data/bot.log 2>/dev/null
