#!/usr/bin/env bash
# Filters text (stdin to stdout) so that no reference to evcc's repository is
# left in it: links, the module path, the organisation name with a slash and
# issue numbers. Used for the test output in upstream-check.yml; the test is
# clean-report-test.sh.
set -eu
sed -E \
  -e 's#https?://[^ )"]*evcc-io/[^ )"]*##g' \
  -e 's#github\.com/evcc-io/evcc#evcc#g' \
  -e 's#evcc-io/#evcc/#g' \
  -e 's/#([0-9]+)/evcc PR \1/g'
