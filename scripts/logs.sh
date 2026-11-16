#!/usr/bin/env bash
# Показывает журнал бота в реальном времени (выход — Ctrl+C).
cd "$(dirname "$0")/.."
touch data/bot.log 2>/dev/null || { mkdir -p data && touch data/bot.log; }
tail -n 50 -f data/bot.log
