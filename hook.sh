#!/usr/bin/env bash
INPUT=$(cat)
curl -s -X POST \
  -H "Content-Type: application/json" \
  -d "$INPUT" \
  --max-time 2 \
  --connect-timeout 1 \
  http://127.0.0.1:7422/hook \
  2>/dev/null || true
