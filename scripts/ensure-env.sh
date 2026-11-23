#!/bin/sh
set -eu
if [ ! -e .env ]; then
  candidate=$(mktemp .env.XXXXXX)
  trap 'rm -f "$candidate"' EXIT HUP INT TERM
  cp .env.example "$candidate"
  chmod 600 "$candidate"
  ln "$candidate" .env 2>/dev/null || test -e .env
fi
