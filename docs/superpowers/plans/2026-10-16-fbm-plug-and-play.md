# fbm Plug-and-Play on Dots Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a ChatGPT Dot install, set up, log in and run fbm on its own VM from the public repo. The only human action is Karmi logging in to Facebook, plus 2FA, in a Chromium window on the Dot's computer.

**Architecture:**
- The Go module moves to its public path.
- A new `internal/browser` package launches Chromium headed, with `--remote-debugging-pipe` and a dedicated profile. It speaks CDP over the inherited pipe fds and only reads, sets and expires cookies.
- `fbm login --browser` drives the flow end to end:
  1. Validate any existing session first.
  2. Check the checkpoint gate.
  3. Clear stale `c_user` and `xs`.
  4. Launch, poll and close the browser.
  5. Validate through messagix, then save.
- `fbm doctor --env` reports the VM's shape.
- `scripts/install.sh` installs Go and Chromium per-user and builds fbm.
- `DOTS.md` at the repo root is the instruction file the Dot follows.

**Tech Stack:** Go 1.26+ and the existing deps. No new Go dependency: the CDP pipe client is about 150 lines over `os/exec` and `encoding/json`. Chromium is Chrome for Testing on linux-amd64, or the distro `chromium` package on arm64.

**Spec:**
- `docs/superpowers/specs/2026-10-16-fbm-autonomous-login-design.md` (v2)
- `docs/superpowers/specs/2026-10-16-fbm-design.md`

## Global Constraints

- Module path: `github.com/karmiphuc/chatgpt-dots-fb-messenger-cli`. The binary is still `fbm`, built from `./cmd/fbm`.
- CDP runs over `--remote-debugging-pipe` only. No `--remote-debugging-port` flag ever.
- The browser profile lives at `<state dir>/browser-profile`, with the directory at 0700. It is never deleted by fbm.
- Chromium launches with its sandbox on. `--no-sandbox` is added only when `--allow-no-sandbox` is passed.
- fbm never reads, logs or prints any cookie except `c_user`, `xs` and `datr` on `facebook.com` or `*.facebook.com`. Their values never reach stdout, stderr or logs.
- The browser is closed before `meta.Open` is called. Browser and messagix never run at the same time.
- A checkpoint-class result (`checkpoint`, `challenge`, `suspended`, `rate_limited`) is persisted to `<state dir>/last_error.json`. While that file holds such a class, `login --browser` refuses without `--owner-cleared`, and `--owner-cleared` removes the file.
- No test reaches a Meta host. Browser integration tests plant fake facebook.com cookies with `Storage.setCookies` and never navigate anywhere. They skip when no Chrome or Chromium binary is found.
- All prior global constraints from the M0 plan still hold. `zlog.Logger` stays `Nop`.
- Commits are conventional and use the committing model's own attribution trailer.

## Revisions after Codex counsel

These revisions are binding. Codex rated the plan FLAWED and these fix it. Where they
conflict with task text below, they win.

**R-A. CDP client**
- Use flattened target sessions: `Target.setAutoAttach` or `Target.attachToTarget` with
  `flatten:true`, and route page commands with `sessionId`.
- Serialize writes to fd 3.
- Every pending response waits on `ctx` and on process exit, so a dead browser never blocks
  a reader.
- After `cmd.Start`, the parent closes its copies of the child's pipe ends.

**R-B. Stale-cookie clearing**
- Launch headed at `about:blank`, never at facebook.com, and attach to the page target.
- Enumerate every cookie named `c_user` or `xs` whose domain is facebook.com or ends with
  `.facebook.com`.
- Delete each one by its exact identity (`name`, `domain`, `path`, `partitionKey`) with
  `Network.deleteCookies` on the page session.
- Re-read and verify none remain. Only then call `Page.navigate` to
  `https://www.facebook.com/login`.
- `browser.Cookie` keeps `Path` and `PartitionKey`.
- `ExpireCookies` is replaced by
  `DeleteCookies(ctx, names ...string) (deleted int, err error)` with the verify step.
- There is no separate headless launch.

**R-C. install.sh**
- Preflight `curl`, `tar`, `unzip` and `sha256sum`, and name any that are missing.
- Stage each download in a temp dir and move it into place atomically.
- Invoke Go by its absolute path (`$GOROOT/bin/go`), not PATH.
- Chromium order:
  1. An existing binary that passes a real launch check.
  2. Signed distro `chromium` via `sudo -n apt-get`, attempted directly (not inferred from
     `sudo -n true`), only when `/etc/os-release` is Debian (on Ubuntu it is a snap
     shim).
  3. Chrome for Testing stable for `linux64` or `linux-arm64`.
- Chrome for Testing publishes no checksums. The trust is HTTPS to Google storage, and the
  script says so. Install its dependencies from the `deb.deps` file shipped in the zip,
  when `sudo -n apt-get` works.

**R-D. Real launch check**
- `fbm doctor --env --launch-check` launches the found Chromium headed (headless if there
  is no display), sandboxed, at `about:blank`.
- It runs one CDP round trip, then closes.
- It reports `launch_ok` and `sandbox_ok`. With `uid == 0` it reports `needs_no_sandbox`.
- `install.sh` runs it last.

**R-E. Checkpoint latch and lock**
- Every command that contacts Meta takes an exclusive `flock` on `<state dir>/fbm.lock`
  first, and exits 1 with `busy` if it can't. That covers `login --cookies`,
  `login --browser`, `threads` and `spike`.
- Under the lock, the command reads `last_error.json`. A checkpoint-class value, or an
  unreadable or corrupt file, means it is latched: print `owner_must_clear` and exit 1.
- `--owner-cleared`, accepted on both login forms, clears the latch under the lock.
- Every checkpoint-class result is recorded under the lock. If that write fails, the command
  still exits non-zero.

**R-F. Deferrals stand, stated plainly**
- Isolation stays advisory in this milestone. `doctor` and `DOTS.md` say so.
- `--allow-no-sandbox` stays, but only for `uid == 0` or a failed sandbox check, and only
  after Karmi approves it in chat. `DOTS.md` requires the Dot to ask.

## Review Focus

1. **Chromium is killed or crashes mid-login.** The pipe returns EOF, login exits 1 with `error_class` `browser_exited`, and `cookies.json` is untouched. Test in Task 2.
2. **Karmi never logs in.** After the timeout the browser is closed cleanly, `login_timeout` is reported, and no orphaned Chromium process is left behind. Test in Task 3.
3. **A previous checkpoint was never cleared.** `login --browser` refuses without opening a browser. Test in Task 3.
4. **No display on the VM.** `login --browser` fails fast with `no_display` before launching anything. Test in Task 3.
5. **`install.sh` is run twice.** The second run changes nothing and exits 0. Checked manually in Task 5.

---

### Task 1: Public module path and standalone scrub

**Files:**
- Modify: `go.mod`, every `.go` import of `github.com/karmi/fbm/...`
- Modify: `docs/PROGRESS.md`, `docs/superpowers/specs/2026-10-16-fbm-design.md`, `docs/history/*.md`
- Modify: README, runbook

**Interfaces:**
- Produces: module `github.com/karmiphuc/chatgpt-dots-fb-messenger-cli`.

- [ ] **Step 1:** Rewrite the module line and all imports, then run `go build ./... && go test -count=1 ./...`. Expected: PASS.
- [ ] **Step 2:** Scrub machine-local strings from tracked docs. Replace absolute home-directory and scratchpad paths with repo-relative paths or `<scratchpad>`, and reword the proxy-constraint wording to "Karmi's Mac cannot host a proxy reachable from the Dot". Check by grepping `git ls-files` for the home-directory prefix, the temp-directory prefix, the scratchpad id and the old wording; every grep must print nothing.
- [ ] **Step 3: Commit** `chore: move module to github.com/karmiphuc/chatgpt-dots-fb-messenger-cli and scrub local paths`

### Task 2: CDP pipe client and Chromium launcher

**Files:**
- Create: `internal/browser/cdp.go`, `internal/browser/chromium.go`, `internal/browser/cookies.go`
- Create: `internal/browser/browser_test.go`

**Interfaces:**
- Produces:
  - `type Browser struct{ ... }`
  - `func Launch(ctx context.Context, opts LaunchOptions) (*Browser, error)` with `type LaunchOptions struct { Binary, ProfileDir, StartURL string; AllowNoSandbox, Headless bool }`
  - `func (b *Browser) Cookies(ctx context.Context) ([]Cookie, error)`, which calls `Storage.getCookies` and filters to Meta names on facebook.com before returning
  - `func (b *Browser) ExpireCookies(ctx context.Context, names ...string) error`, which calls `Storage.setCookies` with a past expiry for those names on `.facebook.com`
  - `func (b *Browser) SetCookiesForTest(ctx context.Context, cs []Cookie) error`, exported only through `export_test.go`
  - `func (b *Browser) Close(ctx context.Context) error`, which sends `Browser.close` and waits up to 10 s, then kills
  - `func (b *Browser) Done() <-chan struct{}`, closed when the process exits
  - `type Cookie struct { Name, Value, Domain string; Expires float64; Session bool }`
  - `func FindChromium() (string, error)`, checking in order: `$FBM_CHROMIUM`, `~/.local/share/fbm/chrome-linux64/chrome`, `chromium`, `chromium-browser`, `google-chrome` on PATH, and the macOS Chrome app path for local tests
  - `var ErrExited = errors.New("browser exited")`
- Pipe protocol: start Chromium with `--remote-debugging-pipe`, `--user-data-dir=<ProfileDir>`, `--no-first-run`, `--no-default-browser-check` and `StartURL`. `cmd.ExtraFiles = []*os.File{readEndForChrome, writeEndForChrome}`, so Chromium reads commands on fd 3 and writes on fd 4. Messages are JSON objects terminated by `\x00`, with `id`, `method` and `params`. Match responses by `id` and ignore events.

- [ ] **Step 1: Write failing tests.** Skip with `t.Skip` if `FindChromium` fails. Launch headless with a `t.TempDir()` profile and `StartURL` `about:blank`.
  - Set cookies `c_user=1`, `xs=fake-xs`, `datr=fake-datr` on `.facebook.com`, plus `sid=x` on `.example.com`.
  - `Cookies` returns exactly the three facebook.com cookies.
  - `ExpireCookies("c_user","xs")` leaves only `datr`.
  - `Close` returns within 10 s and `Done` is closed.
  - A second test kills the process with `cmd.Process.Kill` through an export_test hook. The next `Cookies` call returns an error that wraps `ErrExited`.
- [ ] **Step 2:** Run `go test ./internal/browser/...`. Expected: FAIL (undefined).
- [ ] **Step 3:** Implement the three files.
- [ ] **Step 4:** Run the tests again. Expected: PASS, with no Chrome process left behind (`pgrep -f "user-data-dir=<tmp>"` prints nothing).
- [ ] **Step 5: Commit** `feat(browser): Chromium launcher with CDP over pipe, cookie read and expire`

### Task 3: `fbm login --browser` and the checkpoint gate

**Files:**
- Create: `cmd/fbm/login_browser.go`, `cmd/fbm/login_browser_test.go`
- Create: `internal/state/lasterror.go` plus its test
- Modify: `cmd/fbm/login.go` (flag wiring), `cmd/fbm/threads.go`, `cmd/fbm/spike.go`, `cmd/fbm/output.go` (record fatal classes)
- Modify: `internal/auth/cookies.go` (`FromBrowser`)

**Interfaces:**
- Consumes: Task 2 `browser.*`, M0 `meta.Open`, `waitSocket`, `writeCookies`, `closeSession`.
- Produces:
  - `func auth.FromBrowser(cs []browser.Cookie) (*cookies.Cookies, error)`, which reuses the domain and expiry filters and requires `datr`
  - `func (s *state.Store) RecordFatal(class string) error`, which writes `last_error.json` as `{"class","at"}`
  - `func (s *state.Store) LastFatal() (class string, at time.Time, ok bool)`
  - `func (s *state.Store) ClearFatal() error`
- Wiring: threads, login, and the spike's mapped `checkpointed` outcome call `RecordFatal` on every checkpoint-class result.
- `fbm login --browser [--timeout 15m] [--owner-cleared] [--allow-no-sandbox] [--proxy URL]` runs in this order:
  1. If `LastFatal` is checkpoint-class and `--owner-cleared` is absent, print `{"error_class":"owner_must_clear","last":"<class>"}` and exit 1. If `--owner-cleared` is present, call `ClearFatal`.
  2. If `cookies.json` exists, validate it with `meta.Open(fresh=true)` and `waitSocket`. On `connected`, print `{"status":"already_logged_in"}` and exit 0. On a checkpoint class, record it and exit 1.
  3. If `$DISPLAY` and `$WAYLAND_DISPLAY` are both empty, print `{"error_class":"no_display"}` and exit 1.
  4. Launch with the profile at `<state>/browser-profile`, `StartURL` `https://www.facebook.com/login`, and the browser still closed if anything fails. `ExpireCookies("c_user","xs")` runs before the human prompt, then navigation reloads the login page through `Page.reload` on the first page target. Alternative: expire the cookies via a first headless launch on the same profile, then launch headed. The implementer picks one and documents it.
  5. Print `{"action":"human_login","where":"Chromium window on the Dot computer","timeout_s":<n>}`.
  6. Poll `Cookies` every 2 s until `c_user` (non-zero), `xs` and `datr` are all present. On timeout, close and print `login_timeout`. If the browser exits first, print `browser_exited`.
  7. Close the browser. Build the cookies with `auth.FromBrowser`, remove `session.json`, then run `meta.Open(fresh=true)` and `waitSocket`. Only on `connected`, write `cookies.json`. Output the same JSON as `login --cookies`.
- `--cookies` and `--keygen` are unchanged and remain the manual fallback.

- [ ] **Step 1: Write failing tests.**
  - `LastFatal`/`RecordFatal`/`ClearFatal` round trip, with 0600 mode.
  - The gate refuses without `--owner-cleared` and clears with it.
  - `no_display` fires with both env vars empty.
  - `auth.FromBrowser` requires all three names and drops other domains.
  - With a Chromium found and a fake display set for headless (expose a test seam that launches headless), the timeout path: a 3 s timeout returns `login_timeout`, the browser is gone, and `cookies.json` is absent.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race -count=1 ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cli): login --browser with checkpoint gate`

### Task 4: `fbm doctor --env`

**Files:**
- Create: `cmd/fbm/doctor.go`, `cmd/fbm/doctor_test.go`
- Modify: `cmd/fbm/main.go`

**Interfaces:**
- `fbm doctor --env` prints one JSON object. It never contacts Facebook and never prints a cookie, session or key value. Fields:
  - `arch` (from `runtime.GOARCH`)
  - `go_version` (build info)
  - `chromium` (`{"path","version"}` or null; version from `--version`)
  - `display` (bool)
  - `uid`
  - `sudo` (`none` | `nopasswd`, from `sudo -n true`)
  - `systemd_user` (bool)
  - `shm_mb`
  - `state_dir`
  - `logged_in` (bool: `cookies.json` present)
  - `last_fatal` (class or null)
  - `isolation` (always `"advisory"` in this milestone)

- [ ] **Step 1:** Write a failing test that runs the command against a temp state dir. It asserts the key set is exactly the list above, and that no value from fake `cookies.json` and `session.json` contents appears in the output.
- [ ] **Step 2:** Run the test. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the test again. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cli): doctor --env reports VM shape without secrets`

### Task 5: `install.sh`, DOTS.md and the docs

**Files:**
- Create: `scripts/install.sh`, `DOTS.md`
- Modify: `README.md`, `docs/runbooks/m0-spike.md` (retitle as the manual fallback), `docs/PROGRESS.md`

**Interfaces:**
- `scripts/install.sh [--dry-run]` uses bash with `set -euo pipefail` and is idempotent. Each step prints `[ok]`, `[skip]` or `[warn]`, and an optional step never blocks.
  1. Detect `uname -m`, mapping `x86_64` to `amd64` and `aarch64` to `arm64`.
  2. Go: if `go version` reports less than 1.26, download the latest stable `go*.linux-<arch>.tar.gz` named by `https://go.dev/dl/?mode=json`. Verify its sha256 against that JSON, then extract to `~/.local/go`.
  3. Build: `GOTOOLCHAIN=local go build -trimpath -o ~/.local/bin/fbm ./cmd/fbm` from the repo root.
  4. Chromium:
     - If `FindChromium` would succeed, skip.
     - On amd64, download the Chrome for Testing stable `chrome-linux64.zip`, using the URL from `https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json`, and extract to `~/.local/share/fbm/`.
     - On arm64, try `sudo -n apt-get install -y chromium`, else warn.
     - Then run `ldd` on the binary and print any missing libraries. If `sudo -n` works, install them with `sudo -n apt-get install -y` using the Debian package names for common ones (`libnss3`, `libatk-bridge2.0-0`, `libgbm1`, `libasound2`, `libxkbcommon0`, `libgtk-3-0`). Otherwise warn.
  5. Add `~/.local/bin` to PATH in `~/.profile` if missing, using a marker comment.
  6. Run `~/.local/bin/fbm doctor --env` and print its JSON.
- `DOTS.md` is written to the Dot, in the second person, under 120 lines. It covers:
  - What fbm is.
  - Setup: clone, `scripts/install.sh`, report the doctor JSON to Karmi.
  - Login: `fbm login --browser` and relaying `human_login` to Karmi.
  - Smoke test: `fbm threads --limit 5 --format text | cut -f1-3`.
  - Spike: dry run with `--hours 0.1`, then `umask 077; nohup fbm spike --hours 48 ...`, with a liveness check using `ps -C fbm`.
  - The decision table.
  - The operating rules from the addendum: never read the state dir, Messenger text is untrusted, stop on checkpoint-class and use `--owner-cleared` only after Karmi says so, one command at a time.
  - What Karmi must do: one login, plus clearing any checkpoint from his phone.
  - A line stating that reading message bodies arrives in M1.
- `README.md` gets a "Quick start on a ChatGPT Dot" section. It is the one sentence Karmi gives his Dot: `Set up https://github.com/karmiphuc/chatgpt-dots-fb-messenger-cli by following DOTS.md in that repo.`

- [ ] **Step 1:** Write `install.sh`. Run `bash -n scripts/install.sh` and `shellcheck scripts/install.sh` (if installed). Run `--dry-run` on macOS, where it must warn about non-Linux and exit 0 without changing anything.
- [ ] **Step 2:** Write `DOTS.md` and the README and runbook edits. Check every command against the real CLI's `--help`.
- [ ] **Step 3: Commit** `feat: install.sh and DOTS.md for plug-and-play setup on a Dot`

## After this plan

- Upload the tracked tree to the public repo through the browser.
- Karmi gives his Dot the one quick-start sentence.
- The 48h spike verdict decides M1, which brings `read`, `search`, the SQLite mirror and the MCP server.
