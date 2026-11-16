#!/usr/bin/env bash
# Запускает бота и перезапускает его при падении. Журнал — data/bot.log.
# Обычно запускается через scripts/start.sh (в фоне).
cd "$(dirname "$0")/.."

# В Termux не даём Android усыплять процесс.
command -v termux-wake-lock >/dev/null 2>&1 && termux-wake-lock

if [ ! -x ./assistant ]; then
  echo "Нет ./assistant — сначала соберите: go build -o assistant ." >&2
  exit 1
fi

mkdir -p data
LOG=data/bot.log

while true; do
  # Не даём журналу разрастись больше ~5 МБ.
  if [ -f "$LOG" ] && [ "$(wc -c <"$LOG")" -gt 5000000 ]; then
    mv "$LOG" "$LOG.old"
  fi
  ./assistant -env .env >>"$LOG" 2>&1
  code=$?
  if [ "$code" -eq 0 ]; then
    echo "$(date '+%F %T') бот остановлен" >>"$LOG"
    break
  fi
  echo "$(date '+%F %T') бот завершился с кодом $code, перезапуск через 15 с" >>"$LOG"
  sleep 15
done
