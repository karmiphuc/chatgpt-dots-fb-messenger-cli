#!/usr/bin/env bash
# Make the folder Karmi uploads to the public GitHub repo: exactly the files of
# the current commit, checked before and after copying. Upload only from a
# folder this script made, never from the clone, which also holds fbm state,
# build output and .check-public.local.
#
#   bash scripts/export-public.sh [--out DIR] [--dry-run]
#
# DIR defaults to ~/fbm-publish/<UTC timestamp>/ and must not exist or be
# empty. Refuses when tracked files have uncommitted changes, so the export
# equals a commit. Runs scripts/check-public.sh on the clone first (a hit:
# exit 1, nothing copied) and with --dir on the export after (a hit: exit 1,
# the export is deleted). Prints the folder and the commit sha.
set -euo pipefail

usage() {
	printf 'usage: bash scripts/export-public.sh [--out DIR] [--dry-run]\n'
}

ok() { printf '[ok]   %s\n' "$*"; }
would() { printf '[dry]  would %s\n' "$*"; }
die() {
	printf '[fail] %s\n' "$*" >&2
	exit 1
}

OUT=""
DRY=0
while [ "$#" -gt 0 ]; do
	case $1 in
	--out)
		if [ "$#" -lt 2 ] || [ -z "$2" ]; then
			usage >&2
			exit 2
		fi
		OUT=$2
		shift
		;;
	--dry-run) DRY=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		exit 2
		;;
	esac
	shift
done

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$REPO"
[ "$(git rev-parse --show-toplevel 2>/dev/null)" = "$REPO" ] || die "$REPO is not a git clone"

if [ -z "$OUT" ]; then
	[ -n "${HOME:-}" ] || die "HOME is not set; pass --out DIR"
	OUT=$HOME/fbm-publish/$(date -u +%Y%m%dT%H%M%SZ)
fi
if [ -e "$OUT" ] && [ -n "$(ls -A "$OUT" 2>/dev/null)" ]; then
	die "$OUT already exists and is not empty; pick another --out"
fi

if ! git diff --quiet || ! git diff --cached --quiet; then
	die "tracked files have uncommitted changes; commit or discard them first, so the export equals a commit"
fi
SHA=$(git rev-parse HEAD)

bash "$REPO/scripts/check-public.sh" --quiet-hits ||
	die "check-public.sh found the above in the clone; nothing was exported"
ok "check-public.sh: the clone is clean"

if [ "$DRY" -eq 1 ]; then
	would "copy the files of $SHA into $OUT, then run check-public.sh --dir on it"
	exit 0
fi

mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd -P)
git archive --format=tar HEAD | tar -x -C "$OUT"
ok "exported $(git ls-files | wc -l | tr -d ' ') files of $SHA to $OUT"

if ! bash "$REPO/scripts/check-public.sh" --quiet-hits --dir "$OUT"; then
	rm -rf "$OUT"
	die "check-public.sh found the above in the export; it was deleted"
fi
ok "check-public.sh --dir: the export is clean"

printf '\nUpload the contents of this folder, and nothing else:\n  %s\ncommit: %s\n' "$OUT" "$SHA"
printf 'It holds dot-files (.github, .gitignore, .gitleaks.toml) that Finder hides: Cmd+Shift+. shows them.\n'
