#!/usr/bin/env bash
set -euo pipefail

# Configure the webhook for the WhatsApp Uazapi instance.
#
# Required environment variables:
#   UAZAPI_BASE   - base URL of your Uazapi instance (e.g. https://hia-clientes.uazapi.com)
#   UAZAPI_TOKEN  - token for the instance
#   WEBHOOK_URL   - URL of your webhook endpoint (e.g. https://your-app.up.railway.app/webhook/Leandro-JW)
# Optional:
#   UAZAPI_ADMIN_TOKEN - token for global webhook operations (if you have admin rights)

: "${UAZAPI_BASE:=https://hia-clientes.uazapi.com}"
: "${UAZAPI_TOKEN:?Defina UAZAPI_TOKEN (token da instância)}"
: "${WEBHOOK_URL:?Defina WEBHOOK_URL (ex.: https://example.com/webhook/Leandro-JW)}"
: "${UAZAPI_ADMIN_TOKEN:=}"

echo ">>> Configurando WEBHOOK da instância em: $UAZAPI_BASE"
tmp="$(mktemp)"
http_code=$(curl -sS -w "%{http_code}" -o "$tmp" \
  -X POST "$UAZAPI_BASE/webhook" \
  -H "Accept: application/json" \
  -H "Content-Type: application/json" \
  -H "token: $UAZAPI_TOKEN" \
  -d "{\"url\":\"$WEBHOOK_URL\"}")

if [ "$http_code" -ge 400 ]; then
  echo "Falhou com body {\"url\":...} (HTTP $http_code). Tentando {\"webhook\":...} ..."
  http_code_alt=$(curl -sS -w "%{http_code}" -o "$tmp" \
    -X POST "$UAZAPI_BASE/webhook" \
    -H "Accept: application/json" \
    -H "Content-Type: application/json" \
    -H "token: $UAZAPI_TOKEN" \
    -d "{\"webhook\":\"$WEBHOOK_URL\"}")
  if [ "$http_code_alt" -ge 400 ]; then
    echo "ERRO: não foi possível configurar o webhook (HTTP $http_code_alt). Resposta:"
    cat "$tmp"
    exit 1
  fi
fi

echo "Webhook da instância configurado com sucesso."
echo ">>> Validando com GET $UAZAPI_BASE/webhook ..."
curl -sS "$UAZAPI_BASE/webhook" -H "token: $UAZAPI_TOKEN" | sed -e 's/.*/[instância] &/'

# Optional global webhook configuration
if [ -n "$UAZAPI_ADMIN_TOKEN" ]; then
  echo ">>> (Opcional) Configurando WEBHOOK GLOBAL"
  tmp2="$(mktemp)"
  http_code_g=$(curl -sS -w "%{http_code}" -o "$tmp2" \
    -X POST "$UAZAPI_BASE/globalwebhook" \
    -H "Accept: application/json" \
    -H "Content-Type: application/json" \
    -H "adminToken: $UAZAPI_ADMIN_TOKEN" \
    -d "{\"url\":\"$WEBHOOK_URL\"}")
  if [ "$http_code_g" -ge 400 ]; then
    echo "Aviso: não foi possível definir webhook GLOBAL (HTTP $http_code_g). Resposta:"
    cat "$tmp2"
  else
    echo "Webhook GLOBAL configurado."
    echo ">>> Validando com GET $UAZAPI_BASE/globalwebhook ..."
    curl -sS "$UAZAPI_BASE/globalwebhook" -H "adminToken: $UAZAPI_ADMIN_TOKEN" | sed -e 's/.*/[global] &/'
  fi
fi

echo "Pronto."