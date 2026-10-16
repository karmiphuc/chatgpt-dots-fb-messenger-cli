#!/usr/bin/env bash
# Build fbm and make sure a Chromium it can launch is installed. Linux only.
# Idempotent: a second run changes nothing. --dry-run changes nothing at all.
#
#   bash scripts/install.sh [--dry-run]
#
# Each step prints [ok], [skip] or [warn]. Only a missing Go toolchain with no
# way to fetch one, or a failed build, stops the run; the Chromium steps warn and
# carry on, and the doctor JSON printed last says whether a browser launches.
set -euo pipefail

usage() {
	printf 'usage: bash scripts/install.sh [--dry-run]\n'
}

DRY=0
case "${1:-}" in
"") ;;
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
if [ "$#" -gt 1 ]; then
	usage >&2
	exit 2
fi

ok() { printf '[ok]   %s\n' "$*"; }
skip() { printf '[skip] %s\n' "$*"; }
warn() { printf '[warn] %s\n' "$*"; }
would() { printf '[dry]  would %s\n' "$*"; }
note() { printf '       %s\n' "$*"; }
die() {
	printf '[fail] %s\n' "$*" >&2
	exit 1
}

# The go command must never fetch another toolchain behind our back: go.mod
# carries a toolchain line, and the build uses the Go this script chose.
export GOTOOLCHAIN=local

if [ "$(uname -s)" != Linux ]; then
	warn "install.sh supports Linux only (this is $(uname -s)); nothing was changed"
	exit 0
fi

case "$(uname -m)" in
x86_64 | amd64)
	ARCH=amd64
	CFT=linux64
	;;
aarch64 | arm64)
	ARCH=arm64
	CFT=linux-arm64
	;;
*) die "unsupported architecture $(uname -m): fbm needs amd64 or arm64" ;;
esac

[ -n "${HOME:-}" ] || die "HOME is not set"
REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
[ -f "$REPO/go.mod" ] && [ -d "$REPO/cmd/fbm" ] || die "$REPO is not the fbm repo root"

LOCAL=$HOME/.local
BIN=$LOCAL/bin
FBM=$BIN/fbm
GODIR=$LOCAL/go
CHROME_BASE=$LOCAL/share/fbm
CFT_DIR=chrome-$CFT
CFT_ZIP_URL_PREFIX=https://storage.googleapis.com/chrome-for-testing-public/
CFT_JSON_URL=https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json
GO_JSON_URL='https://go.dev/dl/?mode=json'
PATH_MARK='# fbm: add ~/.local/bin to PATH'

# Downloads are staged under ~/.local so the final mv stays on one filesystem.
STAGE=""
make_stage() {
	if [ -z "$STAGE" ]; then
		mkdir -p "$LOCAL"
		STAGE=$(mktemp -d "$LOCAL/.fbm-install.XXXXXX")
	fi
}
cleanup() {
	if [ -n "$STAGE" ]; then
		rm -rf "$STAGE"
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# fetch URL FILE: HTTPS only.
fetch() {
	curl --proto '=https' --tlsv1.2 -fsSL --retry 3 --connect-timeout 20 -o "$2" "$1"
}

# install_dir SRC DST: move SRC into place; an existing DST is set aside first
# and restored if the move fails.
install_dir() {
	local src=$1 dst=$2 old=""
	mkdir -p "$(dirname "$dst")"
	if [ -e "$dst" ]; then
		old=$STAGE/old.$(basename "$dst")
		mv "$dst" "$old"
	fi
	if ! mv "$src" "$dst"; then
		if [ -n "$old" ]; then
			mv "$old" "$dst"
		fi
		return 1
	fi
}

# as_root CMD...: root runs it directly, otherwise through non-interactive sudo.
as_root() {
	if [ "$(id -u)" -eq 0 ]; then
		"$@"
	elif command -v sudo >/dev/null 2>&1; then
		sudo -n "$@"
	else
		return 127
	fi
}

# run_logged LOGFILE CMD...: run quietly, show the log's tail only on failure.
run_logged() {
	local log=$1 rc=0
	shift
	"$@" >"$log" 2>&1 || rc=$?
	if [ "$rc" -ne 0 ]; then
		tail -n 5 "$log" | while IFS= read -r l; do note "$l"; done
	fi
	return "$rc"
}

# apt_root ARGS...: apt-get as root, non-interactive, with no stdin to read.
apt_root() {
	as_root env DEBIAN_FRONTEND=noninteractive apt-get "$@" </dev/null
}

# apt_update_once: refresh the package lists before the first root apt call of
# the run, so a fresh image does not fail with "Unable to locate package".
APT_UPDATED=0
apt_update_once() {
	[ "$APT_UPDATED" -eq 0 ] || return 0
	APT_UPDATED=1
	make_stage
	if ! run_logged "$STAGE/apt-update.log" apt_root update; then
		warn "apt-get update failed (needs root or passwordless sudo); trying with the existing package lists"
	fi
}

# --- preflight -------------------------------------------------------------

missing=""
for t in curl tar unzip sha256sum; do
	command -v "$t" >/dev/null 2>&1 || missing="$missing $t"
done
have() {
	case " $missing " in
	*" $1 "*) return 1 ;;
	esac
	return 0
}
if [ -z "$missing" ]; then
	ok "preflight: curl, tar, unzip and sha256sum are present"
else
	warn "preflight: missing tools:$missing"
fi

# --- Go --------------------------------------------------------------------

# go_ok GO: succeeds when GO reports Go 1.26.0 or newer. A pre-release suffix
# (1.26rc1) counts as below the release it precedes.
go_ok() {
	local v num pre maj rest min
	v=$("$1" version 2>/dev/null) || return 1
	v=${v#go version go}
	v=${v%% *}
	num=${v%%[!0-9.]*}
	pre=${v#"$num"}
	maj=${num%%.*}
	rest=${num#*.}
	min=${rest%%.*}
	case "$maj$min" in
	"" | *[!0-9]*) return 1 ;;
	esac
	if [ "$maj" -gt 1 ] || { [ "$maj" -eq 1 ] && [ "$min" -gt 26 ]; }; then
		return 0
	fi
	[ "$maj" -eq 1 ] && [ "$min" -eq 26 ] && [ -z "$pre" ]
}

# parse_go_json FILE: print "<filename> <sha256>" of the newest stable
# linux-$ARCH tarball. The JSON is split one token per line so pretty-printed
# and minified feeds read the same.
parse_go_json() {
	tr ',{}[]' '\n' <"$1" | awk -v re="^go[0-9][0-9.]*[.]linux-${ARCH}[.]tar[.]gz\$" '
		/"filename"/ {
			f = $0; sub(/^[^:]*:[ \t]*"/, "", f); sub(/".*$/, "", f)
			hit = (f ~ re)
		}
		/"sha256"/ && hit {
			s = $0; sub(/^[^:]*:[ \t]*"/, "", s); sub(/".*$/, "", s)
			print f, s
			exit
		}'
}

download_go() {
	if ! { have curl && have tar && have sha256sum; }; then
		die "Go 1.26+ is needed and must be downloaded, but these tools are missing:$missing (install them, then re-run)"
	fi
	make_stage
	fetch "$GO_JSON_URL" "$STAGE/go.json" || die "could not fetch $GO_JSON_URL"
	local line file want got
	line=$(parse_go_json "$STAGE/go.json")
	file=${line%% *}
	want=${line#* }
	[ -n "$line" ] || die "no linux-$ARCH Go tarball found in the go.dev feed"
	case "$file" in
	*[!A-Za-z0-9._-]*) die "unexpected Go tarball name in the go.dev feed" ;;
	esac
	case "$want" in
	*[!0-9a-f]* | "") die "unexpected checksum in the go.dev feed" ;;
	esac
	[ "${#want}" -eq 64 ] || die "unexpected checksum length in the go.dev feed"
	fetch "https://go.dev/dl/$file" "$STAGE/$file" || die "could not download $file"
	got=$(sha256sum "$STAGE/$file" | awk '{print $1}')
	[ "$got" = "$want" ] || die "sha256 mismatch for $file: refusing to install it"
	mkdir "$STAGE/gosrc"
	tar -C "$STAGE/gosrc" -xzf "$STAGE/$file" || die "could not extract $file"
	go_ok "$STAGE/gosrc/go/bin/go" || die "downloaded Go is older than 1.26"
	install_dir "$STAGE/gosrc/go" "$GODIR" || die "could not move Go into $GODIR"
	GO=$GODIR/bin/go
	ok "installed ${file%.linux-*} to $GODIR (sha256 verified against go.dev)"
}

step_go() {
	GO=""
	local p
	if p=$(command -v go 2>/dev/null) && [ -n "$p" ] && go_ok "$p"; then
		GO=$p
		skip "Go 1.26+ already at $GO"
	elif [ -x "$GODIR/bin/go" ] && go_ok "$GODIR/bin/go"; then
		GO=$GODIR/bin/go
		skip "Go 1.26+ already at $GO"
	elif [ "$DRY" -eq 1 ]; then
		would "download the latest stable Go for linux-$ARCH from go.dev, verify its sha256, and install it to $GODIR"
		if ! { have curl && have tar && have sha256sum; }; then
			warn "a real run would stop here: Go must be downloaded and tools are missing:$missing"
		fi
	else
		download_go
	fi
}

# --- fbm -------------------------------------------------------------------

step_build() {
	if [ "$DRY" -eq 1 ]; then
		would "build fbm with the Go found above into $FBM (GOTOOLCHAIN=local)"
		return
	fi
	make_stage
	(cd "$REPO" && CGO_ENABLED=0 "$GO" build -trimpath -buildvcs=false -o "$STAGE/fbm" ./cmd/fbm) ||
		die "go build failed"
	mkdir -p "$BIN"
	if [ -x "$FBM" ] && cmp -s "$STAGE/fbm" "$FBM"; then
		skip "fbm at $FBM is already up to date"
	else
		install -m 0755 "$STAGE/fbm" "$FBM.new"
		mv -f "$FBM.new" "$FBM"
		ok "built $FBM"
	fi
}

# --- Chromium --------------------------------------------------------------

# launch_json: the doctor report with the launch check, or nothing on failure.
launch_json() {
	"$FBM" doctor --env --launch-check 2>/dev/null
}

# browser_launches: succeeds when fbm's own launch check says launch_ok.
LAST_LAUNCH_JSON=""
browser_launches() {
	local out
	LAST_LAUNCH_JSON=""
	out=$(launch_json) || return 1
	LAST_LAUNCH_JSON=$out
	case "$out" in
	*'"launch_ok":true'*) return 0 ;;
	esac
	return 1
}

is_debian() {
	# shellcheck disable=SC1091 # os-release is the system's, not ours to follow
	[ -r /etc/os-release ] && [ "$(. /etc/os-release && printf '%s' "${ID:-}")" = debian ]
}

# parse_cft_json FILE: print the Stable channel's chrome-$CFT.zip URL.
parse_cft_json() {
	tr ',{}[]' '\n' <"$1" | awk -v suffix="/chrome-$CFT.zip" '
		/^[ \t]*"Stable"[ \t]*:/ { s = 1; next }
		/^[ \t]*"(Beta|Dev|Canary)"[ \t]*:/ { s = 0; next }
		s && /"url"/ {
			u = $0; sub(/^[^:]*:[ \t]*"/, "", u); sub(/".*$/, "", u)
			if (substr(u, length(u) - length(suffix) + 1) == suffix) { print u; exit }
		}'
}

# apt_chromium: the signed distro package, attempted directly rather than
# inferred from `sudo -n true`. Debian only: on Ubuntu `chromium` is a snap shim.
apt_chromium() {
	make_stage
	apt_update_once
	if run_logged "$STAGE/apt-chromium.log" apt_root install -y chromium; then
		ok "installed the Debian chromium package"
		return 0
	fi
	warn "could not install the Debian chromium package (it needs root or passwordless sudo)"
	return 1
}

# drop_failed_cft: fbm prefers the Chrome for Testing copy over any chromium on
# PATH, so one that fails the launch check would shadow a working browser (and
# be reused forever). When it is the binary the check just used, set it aside;
# the stage dir is deleted on exit.
drop_failed_cft() {
	local bin=$CHROME_BASE/$CFT_DIR/chrome
	[ -d "$CHROME_BASE/$CFT_DIR" ] || return 0
	case "$LAST_LAUNCH_JSON" in
	*"\"path\":\"$bin\""*) ;;
	*) return 0 ;;
	esac
	make_stage
	mv "$CHROME_BASE/$CFT_DIR" "$STAGE/failed-$CFT_DIR"
	ok "removed the Chrome for Testing copy at $CHROME_BASE/$CFT_DIR: it failed the launch check"
}

# cft_chromium: Chrome for Testing stable. Google publishes no checksum for it,
# so the trust is HTTPS to storage.googleapis.com.
cft_chromium() {
	if [ -x "$CHROME_BASE/$CFT_DIR/chrome" ]; then
		skip "Chrome for Testing already at $CHROME_BASE/$CFT_DIR"
		return 0
	fi
	if ! { have curl && have unzip; }; then
		warn "Chrome for Testing needs curl and unzip; missing:$missing"
		return 1
	fi
	make_stage
	fetch "$CFT_JSON_URL" "$STAGE/cft.json" || {
		warn "could not fetch the Chrome for Testing version feed"
		return 1
	}
	local url
	url=$(parse_cft_json "$STAGE/cft.json")
	if [ -z "$url" ]; then
		warn "the Chrome for Testing feed lists no stable $CFT build for this machine"
		return 1
	fi
	case "$url" in
	"$CFT_ZIP_URL_PREFIX"*) ;;
	*)
		warn "the Chrome for Testing feed named a download outside storage.googleapis.com; not fetching it"
		return 1
		;;
	esac
	case "$url" in
	*[!A-Za-z0-9./_:-]*)
		warn "the Chrome for Testing feed gave an unexpected download URL; not fetching it"
		return 1
		;;
	esac
	fetch "$url" "$STAGE/chrome.zip" || {
		warn "could not download Chrome for Testing"
		return 1
	}
	mkdir "$STAGE/cft"
	unzip -q -d "$STAGE/cft" "$STAGE/chrome.zip" || {
		warn "could not unzip Chrome for Testing"
		return 1
	}
	[ -f "$STAGE/cft/$CFT_DIR/chrome" ] || {
		warn "the Chrome for Testing zip has no $CFT_DIR/chrome"
		return 1
	}
	chmod 0755 "$STAGE/cft/$CFT_DIR/chrome"
	install_dir "$STAGE/cft/$CFT_DIR" "$CHROME_BASE/$CFT_DIR" || {
		warn "could not move Chrome for Testing into $CHROME_BASE"
		return 1
	}
	ok "installed Chrome for Testing to $CHROME_BASE/$CFT_DIR"
	note "Google publishes no checksum for it; trust is HTTPS to storage.googleapis.com"
}

# deb_relations FILE: the usable dependencies from a Chrome deb.deps file, one
# per line, as "name [(op version)] | name ...". Every piece is checked against
# a strict Debian relation grammar, because it reaches apt-get as root: a name
# is lowercase alphanumerics with + . - inside and an alphanumeric at both
# ends, so it can never be an apt remove/install operator or a regex.
deb_relations() {
	local rel_re line alt alts out rel
	rel_re='^([a-z0-9][a-z0-9+.-]*[a-z0-9])([[:space:]]*\([[:space:]]*(>=|<=|=|<<|>>)[[:space:]]*([0-9][A-Za-z0-9.+~:-]*)[[:space:]]*\))?$'
	while IFS= read -r line; do
		[ -n "${line//[[:space:]]/}" ] || continue
		out=""
		IFS='|' read -r -a alts <<<"$line"
		for alt in "${alts[@]}"; do
			alt=${alt#"${alt%%[![:space:]]*}"}
			alt=${alt%"${alt##*[![:space:]]}"}
			[[ $alt =~ $rel_re ]] || continue
			rel=${BASH_REMATCH[1]}
			if [ -n "${BASH_REMATCH[3]}" ]; then
				rel="$rel (${BASH_REMATCH[3]} ${BASH_REMATCH[4]})"
			fi
			out=${out:+$out | }$rel
		done
		if [ -n "$out" ]; then
			printf '%s\n' "$out"
		fi
	done < <(tr ',' '\n' <"$1")
}

# apt_has_satisfy: APT 1.9 or newer has `apt-get satisfy`.
apt_has_satisfy() {
	local v major rest minor
	v=$(apt-get --version 2>/dev/null | awk 'NR == 1 { print $2 }')
	major=${v%%.*}
	rest=${v#*.}
	minor=${rest%%[!0-9]*}
	case "$major$minor" in
	"" | *[!0-9]*) return 1 ;;
	esac
	[ "$major" -gt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -ge 9 ]; }
}

# deb_names RELATIONS: for old APT, "install NAME" or "missing NAME" per
# relation line. An alternative already installed satisfies the line; else the
# first that apt-cache resolves is installed; a line with none resolved is
# reported missing, never guessed.
deb_names() {
	local line alt name pick satisfied first alts
	while IFS= read -r line; do
		IFS='|' read -r -a alts <<<"$line"
		pick=""
		first=""
		satisfied=0
		for alt in "${alts[@]}"; do
			alt=${alt#"${alt%%[![:space:]]*}"}
			name=${alt%%[[:space:]]*}
			[ -n "$first" ] || first=$name
			if dpkg-query -W -f='${Status}' "$name" 2>/dev/null | grep -q 'install ok installed'; then
				satisfied=1
				break
			fi
			if [ -z "$pick" ] && apt-cache show "$name" >/dev/null 2>&1; then
				pick=$name
			fi
		done
		[ "$satisfied" -eq 0 ] || continue
		if [ -n "$pick" ]; then
			printf 'install %s\n' "$pick"
		else
			printf 'missing %s\n' "$first"
		fi
	done <<<"$1"
}

# cft_deps: install the shared libraries Chrome for Testing links against, from
# the deb.deps file shipped in its zip, when apt-get can run as root.
cft_deps() {
	local depfile=$CHROME_BASE/$CFT_DIR/deb.deps rels joined plan pkgs unresolved
	if [ ! -r "$depfile" ] || ! command -v apt-get >/dev/null 2>&1; then
		warn "cannot work out Chrome's package dependencies here (no deb.deps or no apt-get); see the ldd report below"
		return 0
	fi
	rels=$(deb_relations "$depfile")
	if [ -z "$rels" ]; then
		warn "deb.deps held no usable package relations; see the ldd report below"
		return 0
	fi
	make_stage
	apt_update_once
	if apt_has_satisfy; then
		joined=$(printf '%s\n' "$rels" | awk 'NR > 1 { printf ", " } { printf "%s", $0 } END { print "" }')
		if run_logged "$STAGE/apt-deps.log" apt_root satisfy -y --no-install-recommends --no-remove "$joined"; then
			ok "installed Chrome's dependencies with apt-get satisfy"
		else
			warn "could not install Chrome's dependencies (needs root or passwordless sudo). As root run: apt-get satisfy -y --no-install-recommends --no-remove \"$joined\""
		fi
		return 0
	fi
	if ! command -v dpkg-query >/dev/null 2>&1 || ! command -v apt-cache >/dev/null 2>&1; then
		warn "cannot check Chrome's dependencies here (no dpkg-query or apt-cache); see the ldd report below"
		return 0
	fi
	plan=$(deb_names "$rels")
	pkgs=$(printf '%s\n' "$plan" | awk '$1 == "install" { printf "%s ", $2 }')
	unresolved=$(printf '%s\n' "$plan" | awk '$1 == "missing" { printf "%s ", $2 }')
	pkgs=${pkgs% }
	unresolved=${unresolved% }
	if [ -n "$unresolved" ]; then
		warn "apt cannot find these Chrome dependencies: $unresolved (an older apt: install them by hand)"
	fi
	if [ -z "$pkgs" ]; then
		if [ -z "$unresolved" ]; then
			skip "every package in deb.deps is already installed"
		fi
		return 0
	fi
	# shellcheck disable=SC2086 # names passed the strict grammar and must split into words
	if run_logged "$STAGE/apt-deps.log" apt_root install -y --no-install-recommends --no-remove $pkgs; then
		ok "installed Chrome's dependencies: $pkgs"
	else
		warn "could not install Chrome's dependencies (needs root or passwordless sudo). As root run: apt-get install -y --no-install-recommends --no-remove $pkgs"
	fi
}

# ldd_report: name any shared library the Chrome for Testing binary cannot find.
ldd_report() {
	local bin=$CHROME_BASE/$CFT_DIR/chrome libs
	command -v ldd >/dev/null 2>&1 || return 0
	[ -x "$bin" ] || return 0
	libs=$(ldd "$bin" 2>/dev/null | awk '/not found/ {print $1}' | tr '\n' ' ')
	if [ -n "$libs" ]; then
		warn "Chrome for Testing still lacks shared libraries: ${libs% }"
	fi
}

step_chromium() {
	if [ "$DRY" -eq 1 ]; then
		would "run fbm doctor --env --launch-check; if no Chromium launches:"
		if is_debian; then
			note "Debian: apt-get update, then apt-get install -y chromium (root or sudo -n), then re-check"
		fi
		note "else Chrome for Testing stable ($CFT) into $CHROME_BASE/$CFT_DIR, then its deb.deps packages via apt-get satisfy (or install on an old apt) when root or sudo -n allows"
		return
	fi
	printf '       checking that Chromium launches (up to ~90 s)\n'
	if browser_launches; then
		skip "an existing Chromium passes the launch check"
		return
	fi
	if [ -n "${FBM_CHROMIUM:-}" ]; then
		warn "FBM_CHROMIUM is set and fails the launch check; fix or unset it. Installing another browser would be ignored"
		return
	fi
	drop_failed_cft
	if is_debian && apt_chromium && browser_launches; then
		ok "the Debian chromium package passes the launch check"
		return
	fi
	if cft_chromium; then
		cft_deps
		ldd_report
		if browser_launches; then
			ok "Chrome for Testing passes the launch check"
			return
		fi
	fi
	warn "no Chromium passes the launch check yet; the doctor report below says why"
}

# --- PATH ------------------------------------------------------------------

step_path() {
	local prof=$HOME/.profile
	if [ -f "$prof" ] && grep -qF "$PATH_MARK" "$prof"; then
		skip "$prof already puts ~/.local/bin on PATH"
		return
	fi
	if [ "$DRY" -eq 1 ]; then
		would "append a PATH line for ~/.local/bin to ~/.profile (after a timestamped .bak)"
		return
	fi
	if [ -f "$prof" ]; then
		cp -p "$prof" "$prof.bak.$(date +%Y%m%d%H%M%S)"
	fi
	{
		printf '\n%s\n' "$PATH_MARK"
		# shellcheck disable=SC2016 # the line is written literally, expanded at login
		printf '%s\n' 'case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH="$HOME/.local/bin:$PATH" ;; esac' 'export PATH'
	} >>"$prof"
	ok "added ~/.local/bin to PATH in ~/.profile (open a new login shell, or export it by hand, to pick it up)"
}

# --- doctor ----------------------------------------------------------------

step_doctor() {
	if [ "$DRY" -eq 1 ]; then
		would "run $FBM doctor --env --launch-check and print its JSON"
		return
	fi
	printf '       running fbm doctor --env --launch-check (up to ~90 s)\n'
	local out
	out=$("$FBM" doctor --env --launch-check) || die "fbm doctor failed: see the message above"
	printf '%s\n' "$out"
	case "$out" in
	*'"launch_ok":true'*) ok "doctor: Chromium launches" ;;
	*) warn "doctor: launch_ok is not true. Do not run login; report this JSON to Karmi" ;;
	esac
}

# --- run -------------------------------------------------------------------

if [ "$DRY" -eq 1 ]; then
	printf '[dry]  --dry-run: nothing below is changed\n'
fi
step_go
step_build
step_chromium
step_path
step_doctor
