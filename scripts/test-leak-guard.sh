#!/usr/bin/env bash
# Plant leaks in throwaway repos and check that the leak guards catch each one:
# check-public.sh in every mode (with and without gitleaks and ripgrep), the
# git hooks, install.sh's push and credential guards, and export-public.sh.
# Nothing here touches this checkout, the network or Facebook; planted values
# are built at run time so this file holds none of their shapes.
#
#   bash scripts/test-leak-guard.sh
#
# Prints [ok] or [fail] per check and exits 1 if any check failed.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Keep the machine's git config, and any hooks it names, out of these repos.
export GIT_CONFIG_GLOBAL=$WORK/gitconfig GIT_CONFIG_NOSYSTEM=1
: >"$GIT_CONFIG_GLOBAL"

PASS=0
FAILS=0
ok() {
	PASS=$((PASS + 1))
	printf '[ok]   %s\n' "$*"
}
bad() {
	FAILS=$((FAILS + 1))
	printf '[fail] %s\n' "$*"
}

# rep CHAR N: CHAR repeated N times.
rep() { printf "%${2}s" '' | tr ' ' "$1"; }

# Planted values, assembled so no line of this file has their shape.
PCT=%3A
XS="12${PCT}$(rep a 10)$(rep B 10)${PCT}2${PCT}1700000000"
AGEKEY="AGE-SECRET-KEY-""1$(rep Q 58)"
AGEPUB="age""1$(rep q 58)"
CUSER="1000$(rep 7 11)"
DATR="$(rep d 12)$(rep D 12)"
DTSG="$(rep t 10):$(rep T 12)"
TKEY="1$(rep 2 15)"
PRIVATE="zq-private-$(rep x 6)"
XSLOWER="12%3a$(rep c 20)%3a2%3a1700000000"
TAB=$(printf '\t')
FBMTEXT="dm${TAB}false${TAB}2026-10-15T09:12:03Z${TAB}Some Thread${TAB}1$(rep 4 15)"
LFS="version https://git-lfs.github.com/spec/""v1"

# Tools for the "no gitleaks" and "no ripgrep" runs: git and the base system.
NOGL=$WORK/bin-nogl
NORG=$WORK/bin-norg
mkdir -p "$NOGL" "$NORG"
for t in git rg; do
	if p=$(command -v "$t"); then
		ln -s "$p" "$NOGL/$t"
	fi
done
ln -s "$(command -v git)" "$NORG/git"
BASEPATH=/usr/bin:/bin:/usr/sbin:/sbin

# new_repo DIR: an empty git repo holding the guard scripts and config.
new_repo() {
	mkdir -p "$1/scripts/githooks"
	git -C "$1" init -q
	git -C "$1" config user.name test
	git -C "$1" config user.email test@example.invalid
	cp "$ROOT/.gitleaks.toml" "$1/"
	cp -p "$ROOT"/scripts/*.sh "$1/scripts/"
	cp -p "$ROOT"/scripts/githooks/* "$1/scripts/githooks/" 2>/dev/null || true
	git -C "$1" add -A
	git -C "$1" commit -q -m base
}

# run_cp DIR PATHVAR ARGS...: run DIR's check-public.sh with PATH set to the
# given dir plus the base system (or the full PATH for "full"). Sets RC and OUT.
run_cp() {
	local dir=$1 bin=$2
	shift 2
	local p=$PATH
	[ "$bin" = full ] || p=$bin:$BASEPATH
	RC=0
	OUT=$(cd "$dir" && PATH=$p bash scripts/check-public.sh "$@" 2>&1) || RC=$?
}

# expect_hit NAME FILE VALUE: FILE is already written (and staged) in repo
# $R. Every mode and toolset must exit 1, name FILE, and never print VALUE
# under --quiet-hits.
expect_hit() {
	local name=$1 file=$2 value=$3 bin mode
	for bin in full "$NOGL" "$NORG"; do
		for mode in tree staged; do
			local args=(--quiet-hits)
			[ "$mode" = staged ] && args+=(--staged)
			run_cp "$R" "$bin" "${args[@]}"
			local tag="$name [$mode, ${bin##*/}]"
			if [ "$RC" -ne 1 ]; then
				bad "$tag: exit $RC, want 1"
			elif ! grep -qF -- "$file" <<<"$OUT"; then
				bad "$tag: output does not name $file"
			elif [ -n "$value" ] && grep -qF -- "$value" <<<"$OUT"; then
				bad "$tag: --quiet-hits printed the matched value"
			else
				ok "$tag: caught"
			fi
		done
	done
	# --dir over a plain copy of the planted file.
	local d=$WORK/dir-$name
	mkdir -p "$d/$(dirname "$file")"
	cp "$R/$file" "$d/$file"
	run_cp "$R" full --quiet-hits --dir "$d"
	if [ "$RC" -eq 1 ] && grep -qF -- "$file" <<<"$OUT" && { [ -z "$value" ] || ! grep -qF -- "$value" <<<"$OUT"; }; then
		ok "$name [dir]: caught"
	else
		bad "$name [dir]: exit $RC"
	fi
	# With no flag, and with --show, the value still never prints.
	local flag
	for flag in "" --show; do
		# shellcheck disable=SC2086 # an empty flag must vanish
		run_cp "$R" full $flag
		if [ "$RC" -eq 1 ] && { [ -z "$value" ] || ! grep -qF -- "$value" <<<"$OUT"; }; then
			ok "$name [${flag:-default} output]: caught, value not shown"
		else
			bad "$name [${flag:-default} output]: exit $RC, or the value was shown"
		fi
	done
}

# expect_clean NAME: repo $R (planted, staged) passes in tree and staged mode.
expect_clean() {
	local args
	for args in "" --staged; do
		# shellcheck disable=SC2086 # empty args must vanish
		run_cp "$R" full --quiet-hits $args
		if [ "$RC" -eq 0 ]; then ok "$1 [${args:-tree}]: allowed"; else bad "$1 [${args:-tree}]: exit $RC: $OUT"; fi
	done
}

# expect_gitleaks NAME FILE: gitleaks with the repo config flags FILE in $R.
expect_gitleaks() {
	command -v gitleaks >/dev/null 2>&1 || {
		printf '[skip] %s: gitleaks not installed\n' "$1"
		return 0
	}
	local rc=0 out
	out=$(cd "$R" && gitleaks dir --config .gitleaks.toml --redact --no-banner --log-level error \
		--report-format template --report-template <(printf '{{range .}}{{.File}} {{.RuleID}}\n{{end}}') \
		--report-path - --exit-code 99 "$2" 2>&1) || rc=$?
	if [ "$rc" -eq 99 ]; then
		ok "$1 [gitleaks]: caught (${out##* })"
	else
		bad "$1 [gitleaks]: exit $rc, want 99"
	fi
}

# plant NAME FILE CONTENT: a fresh repo with FILE holding CONTENT, staged.
plant() {
	R=$WORK/repo-$1
	new_repo "$R"
	mkdir -p "$R/$(dirname "$2")"
	printf '%s\n' "$3" >"$R/$2"
	git -C "$R" add -f -- "$2"
}

printf '== check-public.sh: a clean repo passes\n'
R=$WORK/repo-clean
new_repo "$R"
mkdir -p "$R/internal/auth/testdata"
printf '[{"name": "c_user", "value": "1234567"}]\n' >"$R/internal/auth/testdata/chrome.json"
git -C "$R" add -A
for bin in full "$NOGL" "$NORG"; do
	for args in "" --staged; do
		# shellcheck disable=SC2086 # empty args must vanish
		run_cp "$R" "$bin" --quiet-hits $args
		if [ "$RC" -eq 0 ]; then ok "clean [${args:-tree}, ${bin##*/}]"; else bad "clean [${args:-tree}, ${bin##*/}]: exit $RC: $OUT"; fi
	done
done
run_cp "$R" "$NOGL" --quiet-hits
if grep -qF '[warn] gitleaks not installed; pattern scan only' <<<"$OUT"; then
	ok "no gitleaks: warns and still passes"
else
	bad "no gitleaks: missing the [warn] line"
fi

printf '== check-public.sh: planted leaks\n'
plant xs notes/session.txt "cookie: xs=$XS; path=/"
expect_hit fb-xs notes/session.txt "$XS"
expect_gitleaks fb-xs notes/session.txt

plant xs-netscape notes/jar.txt "$(printf '.facebook.com\tTRUE\t/\tTRUE\t0\txs\t%s' "$XS")"
expect_hit fb-xs-netscape notes/jar.txt "$XS"
expect_gitleaks fb-xs-netscape notes/jar.txt

plant c_user notes/jar2.txt "$(printf '.facebook.com\tTRUE\t/\tTRUE\t0\tc_user\t%s' "$CUSER")"
expect_hit fb-c_user notes/jar2.txt "$CUSER"
expect_gitleaks fb-c_user notes/jar2.txt

plant datr notes/datr.txt "datr=$DATR"
expect_hit fb-datr notes/datr.txt "$DATR"
expect_gitleaks fb-datr notes/datr.txt

plant cookie-json internal/x/testdata/export.json "[{\"name\": \"c_user\", \"domain\": \".facebook.com\", \"value\": \"$CUSER\"}]"
expect_hit fb-cookie-json internal/x/testdata/export.json "$CUSER"
expect_gitleaks fb-cookie-json internal/x/testdata/export.json

plant dtsg notes/dtsg.txt "fb_dtsg=$DTSG"
expect_hit fb-dtsg notes/dtsg.txt "$DTSG"
expect_gitleaks fb-dtsg notes/dtsg.txt

plant age-key notes/key.txt "$AGEKEY"
expect_hit age-secret notes/key.txt "$AGEKEY"
expect_gitleaks age-secret notes/key.txt

plant age-pub notes/recipient.txt "recipient $AGEPUB"
expect_hit age-recipient notes/recipient.txt "$AGEPUB"
expect_gitleaks age-recipient notes/recipient.txt

plant thread notes/threads.txt "{\"thread_key\":$TKEY,\"kind\":\"dm\"}"
expect_hit fbm-output notes/threads.txt "$TKEY"
expect_gitleaks fbm-output notes/threads.txt

plant dyi notes/message_1.txt "{\"sender""_name\": \"Someone\", \"timestamp""_ms\": 1700000000000}"
expect_hit dyi-export notes/message_1.txt "Someone"
expect_gitleaks dyi-export notes/message_1.txt

plant out foo.out "harmless text"
expect_hit name-out foo.out ""

plant cookies cookies.json "[]"
expect_hit name-cookies cookies.json ""

plant inbox messages/inbox/x.json "{}"
expect_hit name-inbox messages/inbox/x.json ""

plant state-json docs/verdict.json "{}"
expect_hit name-json-outside-testdata docs/verdict.json ""

plant profile home/browser-profile/Default/Preferences "{}"
expect_hit name-browser-profile home/browser-profile/Default/Preferences ""

plant lock fbm.lock ""
expect_hit name-lock fbm.lock ""

plant ignore .gitleaksignore ""
expect_hit name-gitleaksignore .gitleaksignore ""

plant big big.txt ""
rep a 307200 >"$R/big.txt"
git -C "$R" add big.txt
expect_hit size-300k big.txt ""

plant private about.md "Written by $PRIVATE at home."
printf '%s\n' "$PRIVATE" >"$R/.check-public.local"
expect_hit private-string about.md "$PRIVATE"

printf '== check-public.sh: modes and errors\n'
R=$WORK/repo-staged
new_repo "$R"
printf 'datr=%s\n' "$DATR" >"$R/later.txt"
run_cp "$R" full --quiet-hits --staged
if [ "$RC" -eq 0 ]; then ok "--staged ignores an unstaged file"; else bad "--staged flagged an unstaged file (exit $RC)"; fi
git -C "$R" add later.txt
printf 'clean\n' >"$R/later.txt"
run_cp "$R" full --quiet-hits --staged
if [ "$RC" -eq 1 ]; then ok "--staged scans the index, not the working file"; else bad "--staged read the working file (exit $RC)"; fi
run_cp "$R" full --quiet-hits --dir "$WORK/does-not-exist"
if [ "$RC" -eq 2 ]; then ok "--dir on a missing directory exits 2"; else bad "--dir on a missing directory: exit $RC, want 2"; fi
R2=$WORK/repo-dash
new_repo "$R2"
printf 'fine\n' >"$R2/-rf"
git -C "$R2" add -- -rf
run_cp "$R2" full --quiet-hits --staged
rc1=$RC
printf 'datr=%s\n' "$DATR" >"$R2/-rf"
git -C "$R2" add -- -rf
run_cp "$R2" full --quiet-hits --staged
if [ "$rc1" -eq 0 ] && [ "$RC" -eq 1 ]; then ok "a file named like a flag is scanned, not parsed"; else bad "a file named -rf: exit $rc1 then $RC: $OUT"; fi
mkdir -p "$WORK/dir-git/.git"
printf 'x\n' >"$WORK/dir-git/.git/config"
run_cp "$R" full --quiet-hits --dir "$WORK/dir-git"
if [ "$RC" -eq 1 ] && grep -qF '.git/config' <<<"$OUT"; then ok "--dir blocks a .git directory"; else bad "--dir with a .git directory: exit $RC"; fi
run_cp "$R" full --bogus
if [ "$RC" -eq 2 ]; then ok "an unknown flag exits 2"; else bad "an unknown flag: exit $RC, want 2"; fi
run_cp "$R" full --staged
if [ "$RC" -eq 1 ] && ! grep -qF "$DATR" <<<"$OUT"; then ok "secret-shaped hits never print their value"; else bad "a secret-shaped hit without --quiet-hits: exit $RC, or it printed the value"; fi

printf '== check-public.sh: round 2 shapes and names\n'
plant xs-lower notes/lower.txt "$(printf '.facebook.com\tTRUE\t/\tTRUE\t0\txs\t%s' "$XSLOWER")"
expect_hit fb-xs-lowercase notes/lower.txt "$XSLOWER"
expect_gitleaks fb-xs-lowercase notes/lower.txt
plant xs-lower-eq notes/lower2.txt "xs=$XSLOWER"
expect_hit fb-xs-lowercase-eq notes/lower2.txt "$XSLOWER"
expect_gitleaks fb-xs-lowercase-eq notes/lower2.txt

plant fbm-text notes/threads.tsv "$FBMTEXT"
expect_hit fbm-text-output notes/threads.tsv "Some Thread"
expect_gitleaks fbm-text-output notes/threads.tsv

plant local-list .check-public.local "anything"
expect_hit name-check-public-local-root .check-public.local ""
plant local-list-deep sub/dir/.check-public.local "anything"
expect_hit name-check-public-local-deep sub/dir/.check-public.local ""

plant dyi-dir export/messages/archived_threads/a/notes.txt "hello"
expect_hit name-dyi-messages-dir export/messages/archived_threads/a/notes.txt ""
plant dyi-file export/x/message_12.HTML "<p>hi</p>"
expect_hit name-dyi-message-file export/x/message_12.HTML ""
plant dyi-inbox-internal internal/x/messages/inbox/a.txt "hi"
expect_hit name-inbox-under-internal internal/x/messages/inbox/a.txt ""
plant go-messages internal/messages/messages.go "package messages"
expect_clean "internal/messages Go package"

plant ext-zip build/OUT.ZIP "x"
expect_hit name-uppercase-zip build/OUT.ZIP ""
plant ext-db data/state.Db "x"
expect_hit name-mixed-case-db data/state.Db ""
for ext in gz tgz tar 7z bz2 xz rar; do
	plant "ext-$ext" "dist/a.$ext" "x"
	expect_hit "name-archive-$ext" "dist/a.$ext" ""
done

plant binary notes/blob.bin ""
printf 'ab\000cd\n' >"$R/notes/blob.bin"
git -C "$R" add notes/blob.bin
expect_hit binary-file notes/blob.bin ""
plant image docs/logo.png ""
printf '\211PNG\r\n\032\n\000\000ok\n' >"$R/docs/logo.png"
git -C "$R" add docs/logo.png
expect_clean "binary image (allowlisted extension)"
plant image-leak docs/shot.PNG ""
printf '\211PNG\000\000\nxs=%s\n\000' "$XS" >"$R/docs/shot.PNG"
git -C "$R" add docs/shot.PNG
expect_hit binary-image-content docs/shot.PNG "$XS"

plant lfs assets/big.psd "$LFS"
expect_hit name-lfs-pointer assets/big.psd ""

plant json-github .github/dependabot.json "{}"
mkdir -p "$R/.vscode"
printf '{}\n' >"$R/.vscode/settings.json"
git -C "$R" add -f .vscode/settings.json
expect_clean "JSON under .github/ and .vscode/"

printf '== check-public.sh: c_user pairs in code and tuples\n'
plant cuser-go internal/x/fixture_test.go "var c = cookie{N: \"c_user\", V: \"$CUSER\"}"
expect_hit fb-c_user-go-literal internal/x/fixture_test.go "$CUSER"
expect_gitleaks fb-c_user-go-literal internal/x/fixture_test.go
plant cuser-tuple notes/pairs.py "pairs = [(\"c_user\", \"$CUSER\")]"
expect_hit fb-c_user-tuple notes/pairs.py "$CUSER"
expect_gitleaks fb-c_user-tuple notes/pairs.py
plant cuser-num notes/state.yaml "\"c_user\": $CUSER"
expect_hit fb-c_user-number notes/state.yaml "$CUSER"
expect_gitleaks fb-c_user-number notes/state.yaml
plant cuser-short internal/x/short_test.go "var c = cookie{N: \"c_user\", V: \"1234567\"}"
expect_clean "a 7-digit c_user pair"

printf '== check-public.sh: symlinks and submodules\n'
R=$WORK/repo-symlink
new_repo "$R"
ln -s ../outside/state "$R/link"
git -C "$R" add link
for args in "" --staged; do
	# shellcheck disable=SC2086 # empty args must vanish
	run_cp "$R" full --quiet-hits $args
	if [ "$RC" -eq 1 ] && grep -q '^link' <<<"$OUT"; then ok "symlink [${args:-tree}]: blocked"; else bad "symlink [${args:-tree}]: exit $RC: $OUT"; fi
done
R=$WORK/repo-gitlink
new_repo "$R"
git -C "$R" update-index --add --cacheinfo "160000,$(git -C "$R" rev-parse HEAD),vendor/sub"
for args in "" --staged; do
	# shellcheck disable=SC2086 # empty args must vanish
	run_cp "$R" full --quiet-hits $args
	if [ "$RC" -eq 1 ] && grep -q '^vendor/sub' <<<"$OUT"; then ok "gitlink [${args:-tree}]: blocked"; else bad "gitlink [${args:-tree}]: exit $RC: $OUT"; fi
done
mkdir -p "$WORK/dir-link"
printf 'x\n' >"$WORK/dir-link/real.txt"
ln -s real.txt "$WORK/dir-link/alias.txt"
run_cp "$R" full --quiet-hits --dir "$WORK/dir-link"
if [ "$RC" -eq 1 ] && grep -q '^alias.txt' <<<"$OUT"; then ok "symlink [dir]: blocked"; else bad "symlink [dir]: exit $RC: $OUT"; fi

printf '== check-public.sh: output never shows private strings or matched text\n'
R=$WORK/repo-show
new_repo "$R"
printf 'by %s\n' "$PRIVATE" >"$R/about.md"
printf 'see /Use''rs/someone/notes\n' >"$R/path.md"
printf '%s\n' "$PRIVATE" >"$R/.check-public.local"
git -C "$R" add about.md path.md
for flag in "" --quiet-hits --show; do
	# shellcheck disable=SC2086 # an empty flag must vanish
	run_cp "$R" full $flag
	if [ "$RC" -eq 1 ] && grep -q '^about.md:1' <<<"$OUT" && ! grep -qF "$PRIVATE" <<<"$OUT"; then
		ok "private string [${flag:-default}]: path:line only"
	else
		bad "private string [${flag:-default}]: exit $RC, or the string was shown"
	fi
done
run_cp "$R" full
if grep -q '^path.md:1' <<<"$OUT" && ! grep -qF 'someone/notes' <<<"$OUT"; then ok "generic pattern [default]: path:line:rule, no text"; else bad "generic pattern [default]: $OUT"; fi
run_cp "$R" full --show
if grep -qF 'someone/notes' <<<"$OUT"; then ok "generic pattern [--show]: full line"; else bad "generic pattern [--show]: no line: $OUT"; fi
# --show never prints a line that also holds a private string or a secret.
printf 'see /Use''rs/someone/a and %s\n' "$PRIVATE" >"$R/both.md"
printf 'see /Use''rs/someone/b and xs=%s\n' "$XS" >"$R/both2.md"
git -C "$R" add both.md both2.md
run_cp "$R" full --show
if [ "$RC" -eq 1 ] && grep -q '^both.md:1' <<<"$OUT" && grep -q '^both2.md:1' <<<"$OUT" &&
	! grep -qF "$PRIVATE" <<<"$OUT" && ! grep -qF "$XS" <<<"$OUT"; then
	ok "generic pattern [--show]: a line that also holds a private string or secret prints path:line only"
else
	bad "generic pattern [--show] with a private string or secret on the line: exit $RC, or the line was shown"
fi
R=$WORK/repo-prose
new_repo "$R"
printf 'A corpo''rate laptop is fine to mention.\n' >"$R/prose.md"
git -C "$R" add prose.md
expect_clean "the word corporate"

printf '== git hooks\n'
R=$WORK/repo-hooks
new_repo "$R"
git -C "$R" config core.hooksPath scripts/githooks
printf 'xs=%s\n' "$XS" >"$R/leak.txt"
git -C "$R" add leak.txt
rc=0
out=$(git -C "$R" commit -q -m leak 2>&1) || rc=$?
if [ "$rc" -ne 0 ] && ! grep -qF "$XS" <<<"$out"; then ok "pre-commit refuses a planted leak quietly"; else bad "pre-commit: exit $rc"; fi
git -C "$R" rm -q --cached leak.txt
rm "$R/leak.txt"
printf 'fine\n' >"$R/ok.txt"
git -C "$R" add ok.txt
if git -C "$R" commit -q -m fine >/dev/null 2>&1; then ok "pre-commit lets a clean commit through"; else bad "pre-commit refused a clean commit"; fi
# A machine-wide hooksPath (a company secret scanner, say) still runs here.
mkdir -p "$WORK/machine-hooks"
printf '#!/bin/sh\ntouch "%s"\n' "$WORK/chained" >"$WORK/machine-hooks/pre-commit"
chmod +x "$WORK/machine-hooks/pre-commit"
git config --global core.hooksPath "$WORK/machine-hooks"
printf 'more\n' >>"$R/ok.txt"
git -C "$R" add ok.txt
if git -C "$R" commit -q -m more >/dev/null 2>&1 && [ -e "$WORK/chained" ]; then ok "pre-commit chains to the global hooksPath"; else bad "pre-commit did not chain to the global hooksPath"; fi
git config --global --unset core.hooksPath
rc=0
(cd "$R" && bash scripts/githooks/pre-push origin https://example.invalid/x.git </dev/null >/dev/null 2>&1) || rc=$?
if [ "$rc" -eq 0 ]; then ok "pre-push passes a clean tree"; else bad "pre-push refused a clean tree (exit $rc)"; fi
git -C "$R" config remote.origin.pushurl DISABLED
rc=0
out=$(cd "$R" && bash scripts/githooks/pre-push origin DISABLED </dev/null 2>&1) || rc=$?
if [ "$rc" -ne 0 ] && grep -qi 'disabled' <<<"$out"; then ok "pre-push refuses when pushing is disabled"; else bad "pre-push with pushurl DISABLED: exit $rc"; fi

printf '== install.sh guards\n'
# A Linux-looking machine: uname and gh are shims, HOME is empty.
SHIM=$WORK/shim
mkdir -p "$SHIM"
cat >"$SHIM/uname" <<'EOF'
#!/bin/sh
case "$1" in -m) echo x86_64 ;; *) echo Linux ;; esac
EOF
# shellcheck disable=SC2016 # the shim expands it, not this script
printf '#!/bin/sh\nexit "${FAKE_GH_RC:-1}"\n' >"$SHIM/gh"
chmod +x "$SHIM/uname" "$SHIM/gh"

# inst_repo DIR: a fake fbm checkout with an origin remote.
inst_repo() {
	new_repo "$1"
	mkdir -p "$1/cmd/fbm"
	: >"$1/go.mod"
	git -C "$1" remote add origin https://example.invalid/fbm.git
}

# run_inst DIR HOME SHIMMED ARGS... (env assignments via `env` before): sets RC, OUT.
run_inst() {
	local dir=$1 home=$2 shim=$3
	shift 3
	local p=$PATH
	[ "$shim" = yes ] && p=$SHIM:$PATH
	RC=0
	OUT=$(env -u GITHUB_TOKEN -u GH_TOKEN -u GH_ENTERPRISE_TOKEN -u FBM_ALLOW_GH_CREDENTIAL \
		-u XDG_CONFIG_HOME -u GH_CONFIG_DIR HOME="$home" PATH="$p" "$@" bash "$dir/scripts/install.sh" --dry-run 2>&1) || RC=$?
}
run_inst_real() {
	local dir=$1 home=$2
	shift 2
	RC=0
	OUT=$(env -u GITHUB_TOKEN -u GH_TOKEN -u GH_ENTERPRISE_TOKEN -u FBM_ALLOW_GH_CREDENTIAL \
		-u XDG_CONFIG_HOME -u GH_CONFIG_DIR HOME="$home" PATH="$SHIM:$PATH" "$@" bash "$dir/scripts/install.sh" 2>&1) || RC=$?
}
cfg() { git -C "$1" config --get-all "$2" 2>/dev/null || true; }

R=$WORK/inst-dry
H=$WORK/home-dry
inst_repo "$R"
mkdir -p "$H"
run_inst "$R" "$H" yes
if [ "$RC" -eq 0 ] && [ -z "$(cfg "$R" core.hooksPath)$(cfg "$R" remote.origin.pushurl)" ] && grep -q 'would.*DISABLED' <<<"$OUT"; then
	ok "install.sh --dry-run on Linux changes nothing and names the push guard"
else
	bad "install.sh --dry-run on Linux: exit $RC: $OUT"
fi

for kind in GH_TOKEN git-credentials hosts.yml gh-auth netrc; do
	R=$WORK/inst-$kind
	H=$WORK/home-$kind
	inst_repo "$R"
	mkdir -p "$H"
	envs=()
	case $kind in
	GH_TOKEN) envs=(GH_TOKEN=planted-token-value) ;;
	git-credentials) printf 'https://%s:%s@%s\n' someone planted-token-value github.com >"$H/.git-credentials" ;;
	hosts.yml) mkdir -p "$H/.config/gh" && printf 'github.com:\n  oauth_token: planted-token-value\n' >"$H/.config/gh/hosts.yml" ;;
	gh-auth) envs=(FAKE_GH_RC=0) ;;
	netrc) printf 'machine api.github.com login someone password planted-token-value\n' >"$H/.netrc" ;;
	esac
	run_inst_real "$R" "$H" ${envs[@]+"${envs[@]}"}
	if [ "$RC" -eq 1 ] && grep -q '^\[fail\] a GitHub credential is present' <<<"$OUT" && ! grep -q planted-token-value <<<"$OUT" &&
		[ "$(cfg "$R" remote.origin.pushurl)" = DISABLED ] && [ "$(cfg "$R" core.hooksPath)" = scripts/githooks ] &&
		git -C "$R" config --local --get credential.helper >/dev/null && [ -z "$(cfg "$R" credential.helper)" ]; then
		ok "install.sh stops on a GitHub credential ($kind), after disabling push"
	else
		bad "install.sh with a credential ($kind): exit $RC: $OUT"
	fi
done

R=$WORK/inst-GH_TOKEN
run_inst_real "$R" "$WORK/home-GH_TOKEN" GH_TOKEN=planted-token-value
if grep -q '^\[skip\].*hooksPath' <<<"$OUT" && grep -q '^\[skip\].*DISABLED' <<<"$OUT" && grep -q '^\[skip\].*credential helper' <<<"$OUT"; then
	ok "install.sh guards are idempotent ([skip] on a second run)"
else
	bad "install.sh second run: $OUT"
fi
run_inst "$R" "$WORK/home-GH_TOKEN" yes GH_TOKEN=planted-token-value FBM_ALLOW_GH_CREDENTIAL=1
if [ "$RC" -eq 0 ] && grep -q '^\[warn\].*GitHub credential' <<<"$OUT"; then
	ok "FBM_ALLOW_GH_CREDENTIAL=1 turns the stop into a [warn]"
else
	bad "FBM_ALLOW_GH_CREDENTIAL=1: exit $RC: $OUT"
fi

R=$WORK/inst-mac
H=$WORK/home-mac
inst_repo "$R"
mkdir -p "$H"
if [ "$(uname -s)" != Linux ]; then
	run_inst "$R" "$H" no
	if [ "$RC" -eq 0 ] && [ -z "$(cfg "$R" core.hooksPath)" ]; then ok "install.sh --dry-run off Linux exits 0 and changes nothing"; else bad "install.sh --dry-run off Linux: exit $RC"; fi
	RC=0
	OUT=$(env HOME="$H" GH_TOKEN=planted-token-value bash "$R/scripts/install.sh" 2>&1) || RC=$?
	if [ "$RC" -eq 0 ] && [ "$(cfg "$R" core.hooksPath)" = scripts/githooks ] && [ -z "$(cfg "$R" remote.origin.pushurl)" ]; then
		ok "install.sh off Linux, no machine-wide hooksPath: sets hooksPath only"
	else
		bad "install.sh off Linux: exit $RC: $OUT"
	fi
fi

# A machine-wide hooksPath (a company secret scanner) must stay in force: no
# local hooksPath over it, on Linux or elsewhere. The runs above had none.
MACHINE_CFG=$WORK/gitconfig-machine
printf '[core]\n\thooksPath = %s\n' "$WORK/machine-hooks" >"$MACHINE_CFG"
R=$WORK/inst-machine-linux
inst_repo "$R"
run_inst_real "$R" "$WORK/home-GH_TOKEN" GIT_CONFIG_GLOBAL="$MACHINE_CFG" GH_TOKEN=planted-token-value
if grep -q '^\[warn\] a machine-wide git hooks path is set' <<<"$OUT" && [ -z "$(cfg "$R" core.hooksPath)" ] &&
	[ "$(cfg "$R" remote.origin.pushurl)" = DISABLED ]; then
	ok "install.sh on Linux leaves a machine-wide hooksPath in force (warns, no local hooksPath)"
else
	bad "install.sh on Linux with a machine-wide hooksPath: exit $RC: $OUT"
fi
R=$WORK/inst-machine-dry
inst_repo "$R"
run_inst "$R" "$WORK/home-dry" yes GIT_CONFIG_GLOBAL="$MACHINE_CFG"
if [ "$RC" -eq 0 ] && grep -q '^\[warn\] a machine-wide git hooks path is set' <<<"$OUT" && ! grep -q 'would set core.hooksPath' <<<"$OUT"; then
	ok "install.sh --dry-run with a machine-wide hooksPath says it will not install the hooks"
else
	bad "install.sh --dry-run with a machine-wide hooksPath: exit $RC: $OUT"
fi
if [ "$(uname -s)" != Linux ]; then
	R=$WORK/inst-machine-mac
	inst_repo "$R"
	RC=0
	OUT=$(env HOME="$H" GIT_CONFIG_GLOBAL="$MACHINE_CFG" bash "$R/scripts/install.sh" 2>&1) || RC=$?
	if [ "$RC" -eq 0 ] && grep -q '^\[warn\] a machine-wide git hooks path is set' <<<"$OUT" && [ -z "$(cfg "$R" core.hooksPath)" ]; then
		ok "install.sh off Linux leaves a machine-wide hooksPath in force"
	else
		bad "install.sh off Linux with a machine-wide hooksPath: exit $RC: $OUT"
	fi
fi

printf '== export-public.sh\n'
R=$WORK/repo-export
new_repo "$R"
printf 'hello\n' >"$R/README.md"
git -C "$R" add README.md
git -C "$R" commit -q -m readme
printf 'untracked\n' >"$R/scratch.txt"
RC=0
OUT=$(cd "$R" && bash scripts/export-public.sh --out "$WORK/export1" 2>&1) || RC=$?
if [ "$RC" -eq 0 ] && [ -f "$WORK/export1/README.md" ] && [ ! -e "$WORK/export1/scratch.txt" ] && [ ! -e "$WORK/export1/.git" ] &&
	[ -x "$WORK/export1/scripts/check-public.sh" ] && grep -qF "$(git -C "$R" rev-parse HEAD)" <<<"$OUT"; then
	ok "export-public copies exactly the commit, keeps the executable bit, prints the sha"
else
	bad "export-public: exit $RC: $OUT"
fi
printf 'changed\n' >>"$R/README.md"
RC=0
OUT=$(cd "$R" && bash scripts/export-public.sh --out "$WORK/export2" 2>&1) || RC=$?
if [ "$RC" -ne 0 ] && [ ! -e "$WORK/export2" ]; then ok "export-public refuses uncommitted changes"; else bad "export-public with a dirty tree: exit $RC"; fi
git -C "$R" checkout -q README.md
printf 'xs=%s\n' "$XS" >"$R/leak.txt"
git -C "$R" add leak.txt
git -C "$R" commit -q --no-verify -m leak
RC=0
OUT=$(cd "$R" && bash scripts/export-public.sh --out "$WORK/export3" 2>&1) || RC=$?
if [ "$RC" -eq 1 ] && [ ! -e "$WORK/export3" ] && ! grep -qF "$XS" <<<"$OUT"; then
	ok "export-public refuses a commit with a leak and copies nothing"
else
	bad "export-public with a leak: exit $RC: $OUT"
fi

R=$WORK/repo-export-link
new_repo "$R"
ln -s /etc/hosts "$R/hosts-link"
git -C "$R" add hosts-link
git -C "$R" commit -q --no-verify -m link
RC=0
OUT=$(cd "$R" && bash scripts/export-public.sh --out "$WORK/export4" 2>&1) || RC=$?
if [ "$RC" -eq 1 ] && [ ! -e "$WORK/export4" ]; then ok "export-public refuses a symlink and copies nothing"; else bad "export-public with a symlink: exit $RC: $OUT"; fi

printf '== repo docs and CI
'
WF=$ROOT/.github/workflows/leak-guard.yml
if ! grep -qE 'secrets\.|CHECK_PUBLIC_EXTRA' "$WF"; then ok "the workflow uses no secrets"; else bad "the workflow uses a secret"; fi
# Every script a doc or hook names to run is run with bash: web uploads drop +x.
# shellcheck disable=SC2016 # a literal $root, as the hooks spell it
unbashed=$(cd "$ROOT" && grep -noE '.{0,12}scripts/[a-z-]+\.sh' README.md AGENTS.md CLAUDE.md DOTS.md scripts/githooks/pre-commit scripts/githooks/pre-push | grep -vE 'bash ("\$root/|~/fbm/)?scripts/' || true)
if [ -z "$unbashed" ]; then ok "docs and hooks run scripts with bash"; else bad "docs or hooks name a script without bash: $unbashed"; fi
for f in DOTS.md AGENTS.md CLAUDE.md; do
	if grep -qF 'Never set FBM_ALLOW_GH_CREDENTIAL yourself; only Karmi may, in chat.' "$ROOT/$f"; then ok "$f forbids setting FBM_ALLOW_GH_CREDENTIAL"; else bad "$f lacks the FBM_ALLOW_GH_CREDENTIAL rule"; fi
done

printf '\n%d passed, %d failed\n' "$PASS" "$FAILS"
[ "$FAILS" -eq 0 ]
