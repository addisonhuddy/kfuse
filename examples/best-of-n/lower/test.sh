#!/usr/bin/env bash
# test.sh — PASS iff parse.sh prints exactly the keys {name, port}.
KEYS=$(./parse.sh config.sample | grep -v '^[[:space:]]*$' | sort)
WANT=$(printf 'name\nport' | sort)
if [ "$KEYS" = "$WANT" ]; then
  echo "PASS"
  exit 0
else
  echo "FAIL: keys=[$(printf '%s' "$KEYS" | tr '\n' ' ')] want=[name port]"
  exit 1
fi
