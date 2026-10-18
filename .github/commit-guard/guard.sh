#!/usr/bin/env bash
# Commit guard for pull requests: usage guard.sh <base> <head>.
# Checks the commits base..head (first parent only, so the commits a merge
# brings in from evcc's releases are not judged):
#   - author must be the fork's owner
#   - messages (without the Co-Authored-By lines) must not contain issue
#     numbers (#123), evcc's organisation name with a slash or a link to evcc's
#     repository
# The same text check runs over the PR title and body, passed as the
# environment variables PR_TITLE and PR_BODY (never interpolated into this
# script). Selftest: guard-selftest.sh. The local git hooks of the working
# folder check the same rule with their own script; keep both in step.
set -u
base=${1:?base}
head=${2:?head}
owner='SolarPower2024 <fuxae92@gmail.com>'
pattern='#[0-9]|evcc-io/|github\.com/evcc-io'
bad=0

for c in $(git log --first-parent --format=%H "$base..$head"); do
  short=$(git log -1 --format=%h "$c")
  author=$(git log -1 --format='%an <%ae>' "$c")
  if [ "$author" != "$owner" ]; then
    echo "::error::commit $short: author is '$author', expected '$owner'"
    bad=1
  fi
  hits=$(git log -1 --format=%B "$c" | grep -vi '^Co-Authored-By:' | grep -E "$pattern" || true)
  if [ -n "$hits" ]; then
    echo "::error::commit $short: message refers to evcc's repository or an issue number"
    echo "$hits" | sed 's/^/    /'
    bad=1
  fi
done

for name in PR_TITLE PR_BODY; do
  hits=$(printf '%s\n' "${!name:-}" | grep -vi '^Co-Authored-By:' | grep -E "$pattern" || true)
  if [ -n "$hits" ]; then
    echo "::error::$name refers to evcc's repository or an issue number"
    echo "$hits" | sed 's/^/    /'
    bad=1
  fi
done

[ "$bad" = 0 ] && echo "commit guard: ok"
exit "$bad"
