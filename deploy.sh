#!/bin/bash
set -e

if [ -f .env ]; then
  export $(grep -v '^#' .env | xargs)
fi

SERVER="${DEPLOY_SERVER:?DEPLOY_SERVER not set in .env}"
REMOTE_DIR="${DEPLOY_DIR:-~/bread_orders}"
DOMAIN="${DEPLOY_DOMAIN:-bread.madpaw.dev}"

echo "→ Синхронизация файлов..."
rsync -avz --delete \
  --exclude='.git/' \
  --exclude='data/' \
  --exclude='*.db' \
  --exclude='bread_orders' \
  --exclude='.env' \
  . "$SERVER:$REMOTE_DIR/"

echo "→ Пересборка и запуск..."
ssh "$SERVER" "cd $REMOTE_DIR && mkdir -p data && docker compose up -d --build"

echo "✓ Готово: https://$DOMAIN"
