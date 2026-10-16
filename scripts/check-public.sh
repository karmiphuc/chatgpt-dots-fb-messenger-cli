#!/usr/bin/env bash
# Fail if a tracked file holds something that must not reach the public repo: a
# machine-local path, an internal name, or an age secret key. Extra strings to
# block, such as a username or an employer name, go one per line in an
# untracked .check-public.local at the repo root; they match as fixed strings,
# ignoring case. Run from anywhere in the checkout, after `git add` of new
# files. Exits 1 on any hit, 2 if the search itself failed.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# The pattern is built from parts so this file does not match itself.
pattern="/Use""rs/|/private/""tmp|claude-""503|corpo""rate|AGE-SECRET-""KEY-1"

errs=$(mktemp)
fixed=$(mktemp)
trap 'rm -f "$errs" "$fixed"' EXIT

local_list=.check-public.local
if [ -e "$local_list" ]; then
	if [ ! -r "$local_list" ]; then
		echo "check-public: cannot read $local_list" >&2
		exit 2
	fi
	# A blank line would match every line, so drop them, and any CR.
	tr -d '\r' <"$local_list" | { grep -v '^[[:space:]]*$' || true; } >"$fixed"
fi

if command -v rg >/dev/null 2>&1; then
	search() { xargs -0 rg -n -i -e "$pattern" --; }
	search_fixed() { xargs -0 rg -n -i -F -f "$fixed" --; }
else
	search() { xargs -0 grep -nIiE -e "$pattern" --; }
	search_fixed() { xargs -0 grep -nIiF -f "$fixed" --; }
fi

# xargs folds "no match" and "search error" into one status, so judge by output.
hits=$(git ls-files -z | search 2>"$errs" || true)
if [ -s "$fixed" ]; then
	more=$(git ls-files -z | search_fixed 2>>"$errs" || true)
	hits=$(printf '%s\n%s\n' "$hits" "$more" | sed '/^$/d')
fi
if [ -s "$errs" ]; then
	cat "$errs" >&2
	echo "check-public: the search failed" >&2
	exit 2
fi
if [ -n "$hits" ]; then
	printf '%s\n' "$hits"
	echo "check-public: the lines above must not be published" >&2
	exit 1
fi
echo "check-public: clean"
