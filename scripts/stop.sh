#!/usr/bin/env bash
# Остановка бота, запущенного через scripts/start.sh.
pkill -f "scripts/run.sh" 2>/dev/null
pkill -TERM -x assistant 2>/dev/null
sleep 1
command -v termux-wake-unlock >/dev/null 2>&1 && termux-wake-unlock
echo "Бот остановлен."
