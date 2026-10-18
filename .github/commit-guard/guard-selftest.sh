#!/usr/bin/env bash
# Selftest of guard.sh in a throwaway repository. Runs in the job commit-guard
# and locally (Git Bash): bash .github/commit-guard/guard-selftest.sh
set -u
guard=$(cd "$(dirname "$0")" && pwd)/guard.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q -b main .
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
org=evcc-io
fails=0

# commit <name> <email> <message>
commit() {
  GIT_AUTHOR_NAME=$1 GIT_AUTHOR_EMAIL=$2 GIT_COMMITTER_NAME=$1 GIT_COMMITTER_EMAIL=$2 \
    git commit -q --allow-empty -m "$3"
}
# expect <name> <exit> -- checks guard.sh base..HEAD, PR_TITLE/PR_BODY from env
expect() {
  local name=$1 want=$2 got
  "$guard" "$base" HEAD >/dev/null 2>&1
  got=$?
  if [ "$got" != "$want" ]; then echo "FAIL $name: exit $got, expected $want"; fails=1; else echo "ok   $name"; fi
}
reset() { git reset -q --hard "$base"; }

commit Anyone anyone@example.com "start"
base=$(git rev-parse HEAD)
owner_name=SolarPower2024; owner_mail=fuxae92@gmail.com

commit $owner_name $owner_mail "clean

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
expect "clean commit with Co-Authored-By" 0
reset

commit Someone else@example.com "wrong author"
expect "wrong author" 1
reset

commit $owner_name $owner_mail "fix, see #123"
expect "issue number" 1
reset

commit $owner_name $owner_mail "see github.com/$org/evcc/core"
expect "organisation name with slash" 1
reset

commit $owner_name $owner_mail "takes over evcc PR 123"
expect "evcc PR 123 in plain text" 0
reset

# merge commit: the second parent carries a foreign author and a number
git checkout -q -b other
commit Someone else@example.com "release commit (#999)"
git checkout -q main
commit $owner_name $owner_mail "own work"
GIT_AUTHOR_NAME=$owner_name GIT_AUTHOR_EMAIL=$owner_mail GIT_COMMITTER_NAME=$owner_name GIT_COMMITTER_EMAIL=$owner_mail \
  git merge -q --no-ff -m "Merge release" other
expect "merge with foreign second parent" 0
reset

commit $owner_name $owner_mail "clean"
PR_TITLE="take over #55" expect "PR title with number" 1
PR_BODY="line
https://github.com/$org/evcc/pull/5" expect "PR body with link" 1
PR_TITLE="take over evcc PR 55" PR_BODY="Generated with Claude Code" expect "PR text clean" 0

[ "$fails" = 0 ] && echo "guard-selftest: ok"
exit "$fails"
