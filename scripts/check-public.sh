#!/usr/bin/env bash
# Fail if a file holds something that must not reach the public repo: a
# machine-local path, an internal name, an age key, a Facebook session cookie,
# fbm output or a Messenger export, or a file whose name, kind or size says it
# came from fbm, a browser profile or a data export (archives, databases,
# binaries other than images, Git LFS pointers, symlinks, submodules). Extra
# strings to block, such as a username or an employer name, go one per line in
# an untracked .check-public.local at the repo root; they match as fixed
# strings, ignoring case, and a hit on one never prints the line. When gitleaks
# is on PATH it runs too, with .gitleaks.toml.
#
#   bash scripts/check-public.sh [--quiet-hits | --show] [--staged | --dir PATH]
#
# With no mode it scans the tracked files (git ls-files) as they are on disk;
# --staged scans what the next commit would hold (the git hooks use it); --dir
# scans everything under PATH (export-public.sh uses it). Each hit prints as
# path:line: rule, or path: blocked file: reason, never the matched text.
# --show adds the line for the built-in local-path pattern only, and never for
# a line or file that any other check flagged; --quiet-hits
# (CI, the hooks) never shows a line. Exits 1 on any hit, 2 if a scanner
# failed or the arguments are wrong.
set -euo pipefail

usage() {
	printf 'usage: bash scripts/check-public.sh [--quiet-hits | --show] [--staged | --dir PATH]\n'
}

QUIET=0
SHOW=0
MODE=tree
DIR=""
while [ "$#" -gt 0 ]; do
	case $1 in
	--quiet-hits) QUIET=1 ;;
	--show) SHOW=1 ;;
	--staged)
		if [ "$MODE" != tree ]; then
			usage >&2
			exit 2
		fi
		MODE=staged
		;;
	--dir)
		if [ "$MODE" != tree ] || [ "$#" -lt 2 ]; then
			usage >&2
			exit 2
		fi
		MODE=dir
		DIR=$2
		shift
		;;
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
if [ "$QUIET" -eq 1 ]; then
	SHOW=0
fi

# Paths given to --dir are relative to where we were started.
if [ "$MODE" = dir ]; then
	if [ ! -d "$DIR" ]; then
		echo "check-public: $DIR is not a directory" >&2
		exit 2
	fi
	DIR=$(cd "$DIR" && pwd)
fi

REPO=$(git rev-parse --show-toplevel)
cd "$REPO"
CONFIG=$REPO/.gitleaks.toml

# Local paths and internal names; the only hits --show prints in full. Built
# from parts so this file does not match itself.
pattern="/Use""rs/|/private/""tmp|claude-""503"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
errs=$TMP/errs
fixed=$TMP/fixed
list=$TMP/list
hitfile=$TMP/hits
: >"$errs"
: >"$fixed"
: >"$list"
: >"$hitfile"

# Secret shapes, one ERE per line, the same shapes as .gitleaks.toml so the
# check holds without gitleaks. Matched case-sensitively; never printed.
shapes=$TMP/shapes
B='(^|[^A-Za-z0-9_])'
E='([^A-Za-z0-9_]|$)'
S='[[:space:]]*'
Q="[\"']?"
P='%3[Aa]'
T=$(printf '\t')
{
	printf '%s\n' "${B}xs${Q}${S}[=:]${S}${Q}[0-9]{1,3}(${P}|:)[A-Za-z0-9_-]{16,}(${P}|:)[0-9](${P}|:)[0-9]{10}"
	printf '%s\n' "${B}[0-9]{1,3}${P}[A-Za-z0-9_-]{16,}${P}[0-9]${P}[0-9]{10}"
	printf '%s\n' "${B}c_user${Q}${S}[=:[:space:]]${S}${Q}[0-9]{8,20}${E}"
	printf '%s\n' "${B}c_user[^0-9]{0,30}[^A-Za-z0-9_][0-9]{8,20}${E}"
	printf '%s\n' "${B}datr${Q}${S}[=:[:space:]]${S}${Q}[A-Za-z0-9_-]{24}${E}"
	printf '%s\n' "\"name\"${S}:${S}\"(c_user\"[^}]{0,80}\"value\"${S}:${S}\"[0-9]{8,20}\"|xs\"[^}]{0,80}\"value\"${S}:${S}\"[0-9]{1,3}(${P}|:)[A-Za-z0-9_-]{16,}|datr\"[^}]{0,80}\"value\"${S}:${S}\"[A-Za-z0-9_-]{24}\")"
	printf '%s\n' "fb_dtsg${Q}${S}[=:]${S}${Q}[A-Za-z0-9_:-]{20,}"
	printf '%s\n' "AGE-SECRET-KEY-[1][A-Z0-9]{58}"
	printf '%s\n' "[Aa][Gg][Ee]-[Ss][Ee][Cc][Rr][Ee][Tt]-[Kk][Ee][Yy]-1"
	printf '%s\n' "${B}age1[a-z0-9]{58}${E}"
	printf '%s\n' "\"thread_key\"${S}:${S}\"?[0-9]{13,20}"
	printf '%s\n' "^(dm|group|other)${T}(true|false)${T}[0-9]{4}-[0-9]{2}-[0-9]{2}T[^[:space:]]+${T}[^${T}]*${T}[0-9]{13,20}\$"
	printf '%s\n' "\"sender_name\"${S}:"
	printf '%s\n' "\"timestamp_ms\"${S}:${S}[0-9]{12,}"
} >"$shapes"

local_list=$REPO/.check-public.local
if [ -e "$local_list" ]; then
	if [ ! -r "$local_list" ]; then
		echo "check-public: cannot read $local_list" >&2
		exit 2
	fi
	# A blank line would match every line, so drop them, and any CR.
	tr -d '\r' <"$local_list" | { grep -v '^[[:space:]]*$' || true; } >"$fixed"
fi

hit() { printf '%s\n' "$*" >>"$hitfile"; }

# --- the files to scan ---------------------------------------------------------
#
# ROOT is a directory and $list holds NUL-separated paths relative to it. The
# tree and the index are copied into a mirror, so every scanner (gitleaks
# included) sees exactly the files that would be published, and nothing else.
# Symlinks and submodules are hits and are never copied or followed.

# mirror_add SRC REL: copy SRC to $ROOT/REL.
mirror_add() {
	mkdir -p "$ROOT/$(dirname -- "$2")"
	cp -- "$1" "$ROOT/$2"
}

# kind_hit MODE PATH: a hit when a git mode is a symlink or a submodule.
kind_hit() {
	case $1 in
	120000) hit "$2: blocked file: a symlink" ;;
	160000) hit "$2: blocked file: a submodule (gitlink)" ;;
	*) return 1 ;;
	esac
}

case $MODE in
tree)
	ROOT=$TMP/mirror
	mkdir "$ROOT"
	while IFS= read -r -d '' rec; do
		meta=${rec%%"$T"*}
		p=${rec#*"$T"}
		if kind_hit "${meta%% *}" "$p"; then
			continue
		fi
		if [ -L "$p" ]; then
			hit "$p: blocked file: a symlink"
		elif [ -f "$p" ]; then
			# A tracked file deleted from disk is not published by an upload.
			mirror_add "$p" "$p"
			printf '%s\0' "$p" >>"$list"
		fi
	done < <(git ls-files -s -z)
	;;
staged)
	ROOT=$TMP/mirror
	mkdir "$ROOT"
	while IFS= read -r -d '' meta && IFS= read -r -d '' p; do
		# ":oldmode newmode oldsha newsha status"
		# shellcheck disable=SC2086 # split the five fields
		set -- $meta
		if kind_hit "$2" "$p"; then
			continue
		fi
		mkdir -p "$ROOT/$(dirname -- "$p")"
		git show ":$p" >"$ROOT/$p"
		printf '%s\0' "$p" >>"$list"
	done < <(git diff --cached --raw -z --no-renames --diff-filter=ACMR)
	;;
dir)
	ROOT=$DIR
	while IFS= read -r -d '' p; do
		p=${p#./}
		if [ -L "$ROOT/$p" ]; then
			hit "$p: blocked file: a symlink"
		else
			printf '%s\0' "$p" >>"$list"
		fi
	done < <(cd "$ROOT" && find . \( -type f -o -type l \) -print0)
	;;
esac

# --- names, kinds and sizes --------------------------------------------------------

shopt -s nocasematch
DYI_FILE='^message_[0-9]+\.(json|html)$'

# name_reason PATH: why a file with this path must not be published, if it
# must not. Matching ignores case.
name_reason() {
	local p=/$1 base=${1##*/}
	case $base in
	*.out | *.db | *.db-* | *.sqlite* | *.ldb | *.har | *.age | *.key)
		echo "fbm output, a database, a HAR or a key file"
		return
		;;
	*.zip | *.gz | *.tgz | *.tar | *.7z | *.bz2 | *.xz | *.rar)
		echo "an archive"
		return
		;;
	cookies*.json | cookies*.txt | session.json | last_error.json | spike-verdict.json | fbm.lock)
		echo "an fbm state file"
		return
		;;
	.check-public.local)
		echo "the private string list"
		return
		;;
	"Login Data"* | Cookies | "Web Data" | "Local State")
		echo "a browser profile file"
		return
		;;
	.gitleaksignore)
		echo "a gitleaks ignore list (it would silence the leak scan)"
		return
		;;
	esac
	if [[ $base =~ $DYI_FILE ]]; then
		echo "a Facebook data export"
		return
	fi
	case $p in
	*/browser-profile/*)
		echo "a browser profile file"
		return
		;;
	*/messages/inbox/* | */your_facebook_activity/*)
		echo "a Facebook data export"
		return
		;;
	*/.git/*)
		echo "a .git directory (publish an export-public.sh folder instead)"
		return
		;;
	esac
	case $p in
	*/messages/*)
		case $1 in
		internal/*) ;;
		*)
			echo "a Facebook data export (a messages/ folder outside internal/)"
			return
			;;
		esac
		;;
	esac
	case $1 in
	*.json)
		case $1 in
		internal/*/testdata/* | .github/* | .vscode/*) ;;
		*)
			echo "JSON outside internal/*/testdata/, .github/ and .vscode/"
			return
			;;
		esac
		;;
	esac
}

# kind_reason PATH SIZE: why the file's contents make it unpublishable: a
# binary (a NUL byte) that is not an image, or a Git LFS pointer.
kind_reason() {
	local f=$ROOT/$1 text
	text=$(LC_ALL=C tr -d '\000' <"$f" | wc -c | tr -d ' ')
	if [ "$text" -ne "$2" ]; then
		case $1 in
		*.png | *.jpg | *.jpeg | *.gif | *.ico | *.webp) ;;
		*) echo "a binary file (only png, jpg, jpeg, gif, ico and webp images may be binary)" ;;
		esac
		return
	fi
	case $(head -c 64 "$f") in
	"version https://git-lfs.github.com/spec/"*) echo "a Git LFS pointer" ;;
	esac
}

while IFS= read -r -d '' p; do
	size=$(wc -c <"$ROOT/$p" | tr -d ' ')
	reason=$(name_reason "$p")
	if [ -z "$reason" ]; then
		reason=$(kind_reason "$p" "$size")
	fi
	if [ -n "$reason" ]; then
		hit "$p: blocked file: $reason"
	fi
	if [ "$p" != go.sum ] && [ "$size" -gt 204800 ]; then
		hit "$p: blocked file: larger than 200 KB ($size bytes)"
	fi
done <"$list"
shopt -u nocasematch

# --- contents ----------------------------------------------------------------------
#
# Binary files are searched as text (-a), so an image cannot hide a cookie.

cd "$ROOT"
if command -v rg >/dev/null 2>&1; then
	search_names() { xargs -0 rg -H -n -a -i -e "$pattern" --; }
	search_fixed() { xargs -0 rg -H -n -a -i -F -f "$fixed" --; }
	search_shapes() { xargs -0 rg -H -n -a -f "$shapes" --; }
else
	search_names() { xargs -0 grep -HnaiE -e "$pattern" --; }
	search_fixed() { xargs -0 grep -HnaiF -f "$fixed" --; }
	search_shapes() { xargs -0 grep -HnaE -f "$shapes" --; }
fi

# label RULE: path:line: RULE for each grep hit, dropping the matched text.
label() { cut -d: -f1,2 | sed "s/\$/: $1/"; }

# xargs folds "no match" and "search error" into one status, so judge by stderr.
if [ -s "$list" ]; then
	if [ "$SHOW" -eq 1 ]; then
		# Full lines wait until every other hit is known: see the end.
		search_names <"$list" 2>>"$errs" >"$TMP/shown" || true
	else
		search_names <"$list" 2>>"$errs" | label "local path or internal name" >>"$hitfile" || true
	fi
	if [ -s "$fixed" ]; then
		search_fixed <"$list" 2>>"$errs" | label "private string (.check-public.local)" >>"$hitfile" || true
	fi
	search_shapes <"$list" 2>>"$errs" | label "secret-shaped value" >>"$hitfile" || true
fi
if [ -s "$errs" ]; then
	cat "$errs" >&2
	echo "check-public: the search failed" >&2
	exit 2
fi

# --- gitleaks ----------------------------------------------------------------------

if command -v gitleaks >/dev/null 2>&1; then
	if [ ! -r "$CONFIG" ]; then
		echo "check-public: cannot read $CONFIG" >&2
		exit 2
	fi
	# The report names file, line and rule only: never the secret.
	printf '{{range .}}{{.File}}:{{.StartLine}}: gitleaks {{.RuleID}}\n{{end}}' >"$TMP/tpl"
	glrc=0
	gitleaks dir --config "$CONFIG" --redact --no-banner --ignore-gitleaks-allow \
		--gitleaks-ignore-path "$TMP/no-ignore-file" --log-level error \
		--report-format template --report-template "$TMP/tpl" --report-path "$TMP/gitleaks" \
		--exit-code 99 . >/dev/null 2>"$TMP/gitleaks.err" || glrc=$?
	case $glrc in
	0) ;;
	99) sed 's#^\./##' "$TMP/gitleaks" >>"$hitfile" ;;
	*)
		sed 's/^/gitleaks: /' "$TMP/gitleaks.err" >&2
		echo "check-public: gitleaks failed (exit $glrc)" >&2
		exit 2
		;;
	esac
else
	echo "[warn] gitleaks not installed; pattern scan only" >&2
fi

# --show: a full line prints only when nothing else hit that line or its file,
# so a private string, a secret or a blocked file's contents never print.
if [ -s "$TMP/shown" ]; then
	awk -v hits="$hitfile" '
		BEGIN {
			while ((getline h < hits) > 0) {
				i = index(h, ": blocked file: ")
				if (i) { blocked[substr(h, 1, i - 1)] = 1; continue }
				split(h, f, ":"); seen[f[1] ":" f[2]] = 1
			}
		}
		{
			split($0, f, ":"); k = f[1] ":" f[2]
			if ((k in seen) || (f[1] in blocked)) print k ": local path or internal name"
			else print
		}' "$TMP/shown" >>"$hitfile"
fi

if [ -s "$hitfile" ]; then
	sort -u "$hitfile"
	echo "check-public: the files or lines above must not be published" >&2
	exit 1
fi
echo "check-public: clean"
