#!/usr/bin/env bash
# Test of clean-report.sh: after the filter the guard pattern of
# upstream-check.yml ('#[0-9]|evcc-io/') must not match any more.
set -eu
dir=$(cd "$(dirname "$0")" && pwd)
org=evcc-io
sample=$(printf '%s\n' \
  "FAIL	github.com/$org/evcc/core	3.2s" \
  "--- FAIL: TestX (0.01s)" \
  "panic: runtime error: index out of range" \
  "core/site.go:12:3: cannot use x (variable of type github.com/$org/evcc/api.Charger) as int" \
  "core/site_test.go:40: see #1234 and https://github.com/$org/evcc/pull/1234" \
  "FAIL")
out=$(printf '%s\n' "$sample" | "$dir/clean-report.sh")
if printf '%s\n' "$out" | grep -qE "#[0-9]|$org/"; then
  echo "clean-report-test: guard pattern still matches:"; printf '%s\n' "$out"; exit 1
fi
for want in "FAIL	evcc/core	3.2s" "--- FAIL: TestX" "panic:" "evcc PR 1234" "type evcc/api.Charger"; do
  printf '%s\n' "$out" | grep -qF -- "$want" || { echo "clean-report-test: missing '$want'"; printf '%s\n' "$out"; exit 1; }
done
echo "clean-report-test: ok"
