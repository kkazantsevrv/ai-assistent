#!/usr/bin/env bash
# Установка и сборка ассистента в Termux (Android).
# Запуск:  bash scripts/termux-install.sh
set -e

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

if [ -z "$PREFIX" ] || [ ! -d "$PREFIX" ]; then
  echo "Похоже, это не Termux. На ПК соберите так: go build -o assistant ."
  exit 1
fi

echo "==> Устанавливаю пакеты (golang, git, procps)…"
pkg update -y
pkg install -y golang git procps

echo "==> Собираю бинарник (первый раз может занять несколько минут)…"
export GOTOOLCHAIN=local
go build -trimpath -ldflags "-s -w" -o assistant .
echo "    готово: $ROOT/assistant"

if [ ! -f .env ]; then
  cp .env.example .env
  echo "==> Создан файл .env — впишите в него токены:"
  echo "    nano $ROOT/.env"
fi

# Автозапуск при включении телефона через приложение Termux:Boot (если установлено).
BOOT_DIR="$HOME/.termux/boot"
read -r -p "Настроить автозапуск через Termux:Boot? [y/N] " ans
if [[ "$ans" =~ ^[YyДд]$ ]]; then
  mkdir -p "$BOOT_DIR"
  cat > "$BOOT_DIR/start-assistant.sh" <<EOF
#!/data/data/com.termux/files/usr/bin/sh
termux-wake-lock
cd "$ROOT" && nohup bash scripts/run.sh >/dev/null 2>&1 &
EOF
  chmod +x "$BOOT_DIR/start-assistant.sh"
  echo "    создан $BOOT_DIR/start-assistant.sh"
  echo "    установите приложение Termux:Boot (F-Droid) и один раз откройте его."
fi

chmod +x scripts/*.sh
echo
echo "Дальше:"
echo "  1) nano .env              — токен бота и ключ DeepSeek"
echo "  2) bash scripts/start.sh  — запустить в фоне"
echo "  3) bash scripts/logs.sh   — смотреть журнал"
echo "  4) bash scripts/stop.sh   — остановить"
