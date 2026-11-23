#!/bin/sh
set -eu
test "$(wc -l < VERSION | tr -d ' ')" = 1
LC_ALL=C grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' VERSION
for script in scripts/*.sh; do sh -n "$script"; done
test -f .env.example
test -f go.sum
test -z "$(git ls-files .env '.runtime/*' '.build/*' '*test-results*')"
