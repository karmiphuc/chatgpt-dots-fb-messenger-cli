# Manual fallback: 48-hour login spike with a cookie export

This is the fallback path. The normal route is [DOTS.md](../../DOTS.md): the Dot installs fbm, Karmi logs in
to Facebook in a Chromium window on the Dot's computer (`fbm login --browser`), and the Dot runs the spike.
Use this runbook only when that cannot work, for example the computer has no display or Chromium will not
launch. Here the cookies are exported on a Mac and travel age-encrypted through the chat. After a
checkpoint, every command refuses with `owner_must_clear` until Karmi has cleared it from his phone and the
Dot passes `--owner-cleared` to `fbm login --cookies`.

Question M0 answers: does Karmi's Facebook account, connected from the Dot VM's own IP, survive 48 hours
idle-connected without a checkpoint? Nothing else is being tested. M1 starts only after a `survived` verdict.

The only channel to the VM is the ChatGPT chat: Karmi uploads files and asks the Dot to run commands.

```
Mac (Karmi)                          Chat                       Dot VM
-----------                          ----                       ------
make linux-amd64  ------ upload fbm ------------------------>   install binary
                                                                 fbm login --keygen
                  <---- public key (safe to read) ------------
cookies -> age -r <pubkey>
                  ------ upload cookies.age ---------------->   fbm login --cookies
                                                                 fbm threads (smoke)
                                                                 fbm spike --hours 0.1 (dry run)
                                                                 fbm spike --hours 48 (nohup)
                  <---- fbm status JSON, verbatim ------------
```

## Brief the Dot first

Paste this at the start of the chat, before any command:

> I am going to ask you to run a few commands on your VM, one at a time. Rules for the whole session:
> 1. Run each command exactly as I give it. Do not add flags, retry, or "fix" anything on your own.
> 2. Report stdout and stderr verbatim in a code block. Do not summarise or paraphrase them.
> 3. Never `cat`, `head`, `tail`, `less`, `grep`, or otherwise open any file under `~/.local/state/fbm/`
>    or `$XDG_STATE_HOME/fbm/`. Those files hold secrets. Use `fbm status` to inspect state.
> 4. If a command fails, stop and show me the output. Do not try a different approach.

## Where state lives

`$XDG_STATE_HOME/fbm`, or `~/.local/state/fbm` when `XDG_STATE_HOME` is unset. Directory mode 0700, files 0600.
It holds the age private key, the cookies, the messagix session state and the spike verdict. The binary
refuses to run if this directory (or any parent) is inside a git checkout, so do not point
`XDG_STATE_HOME` into a repo. Never copy anything out of it.

## Prerequisites

- 2FA is on for the Facebook account. Karmi confirms this in chat before starting. Check under Accounts Center, Password and
  security, Two-factor authentication (menu names change). `fbm` cannot check it.
- A phone with the Facebook app installed and logged in. If Facebook checkpoints the account during the run,
  you clear it from the phone.
- `age` on the Mac: `brew install age`.
- Pause or clear any clipboard-history tool (Raycast, Alfred, Paste) before copying cookies: they retain
  copied text, so the cookie JSON would sit in their history.
- You can be logged in to facebook.com in a desktop browser (Chrome or similar), as Karmi.
- Do not log out of facebook.com in that browser from now until the spike ends. Logging out invalidates the
  exported session cookies and the spike would read as `logged_out`.
- Reserve 48 hours where the account is used normally, from the phone and browser as usual. Do not change the
  password or log in from new places during the run: that is a confound.

## Build

The Mac needs Go 1.26 or newer (`brew install go`). `go.mod` pins `toolchain go1.27.1`, which Go downloads
on first build; a managed Mac may block that download. If the build fails fetching the toolchain, install
Go 1.27.x directly from the go.dev/dl installer and build again.

On the Mac, in the repo root:

```sh
make linux-amd64
shasum -a 256 dist/fbm-linux-amd64
```

Find the VM architecture first: ask the Dot to run `uname -m`. `x86_64` means amd64 (use the target above).
`aarch64` means arm64: `make linux-arm64`, and use `dist/fbm-linux-arm64` below.

Note the SHA-256. Output is a static binary, about 19 MB.

Fallback, only if the Dot already has Go 1.26 or newer and the repo source: `go install ./cmd/fbm` on the VM
puts the binary at `~/go/bin/fbm`. Skip the next section's `install` step and use that path instead of
`~/bin/fbm`. Cross-building on the Mac is the default because it needs no source on the VM.

## Deliver binary

1. Upload `dist/fbm-linux-amd64` (or `-arm64`) in the chat.
2. Ask the Dot to run, replacing `<UPLOADED_PATH>` with the path where the Dot saved the upload (ask it to
   tell you):

```sh
mkdir -p ~/bin ~/fbm-m0 && install -m 0755 <UPLOADED_PATH> ~/bin/fbm && sha256sum ~/bin/fbm && ~/bin/fbm --help
```

3. The hash must equal the one from the Mac. `fbm --help` prints a usage list with `login`, `threads`,
   `status`, `spike`. If the hash differs, stop: the upload was altered or truncated.

If the chat refuses the upload as an unrecognized source, confirm it once in the UI; the binary is not secret.

All commands below use `~/bin/fbm`. Run them from `~/fbm-m0`.

## Keygen on VM

```sh
cd ~/fbm-m0 && ~/bin/fbm login --keygen
```

Expected stdout, one line:

```json
{"public_key":"age1..."}
```

The public key is safe to read in the transcript. Copy it. The private key stays on the VM in the state
directory (`fbm.key`, mode 0600). Keygen never overwrites: running it twice fails with "file exists", which
is correct. Do not delete `fbm.key` to retry unless the Dot confirms no `cookies.age` was encrypted to the
old key.

## Export cookies on Mac

1. In the desktop browser, open `https://www.facebook.com` while logged in as Karmi. Make sure the page is
   facebook.com, not another tab.
2. Install the Cookie-Editor browser extension if it is not there. Open it on the facebook.com tab.
3. Choose Export, format JSON. The extension copies the JSON to the clipboard. Export only facebook.com: do
   not export "all sites".
4. The JSON contains live session credentials. Never paste it into the chat, Slack, or any note. Only the
   encrypted `.age` file is ever uploaded.

If your Cookie-Editor version offers a file download instead of the clipboard, use it, and delete the file
after the next section.

## Encrypt

With the JSON still on the clipboard, encrypt straight from the clipboard so no plaintext file is written:

```sh
cd ~/Downloads && pbpaste | age -r <PUBKEY> -a -o cookies.age; pbcopy < /dev/null
```

Replace `<PUBKEY>` with the `age1...` key from Keygen. The clipboard is cleared whether or not `age` succeeded (`;`, not `&&`).

If you saved a `cookies.json` file instead: `age -r <PUBKEY> -a -o cookies.age cookies.json`, then
`rm ~/Downloads/cookies.json`. Check that no `cookies*.json` is left in Downloads.

`cookies.age` should start with `-----BEGIN AGE ENCRYPTED FILE-----`. Open it with `head -c 40 cookies.age`
to confirm. Do not open anything else.

## Deliver `cookies.age`

1. Upload `cookies.age` in the chat. Only this file.
2. Ask the Dot where it saved it, and to confirm the path with `ls -l <UPLOADED_PATH>`. Do not ask it to open
   the file.

## Login

Ask the Dot to run, replacing `<UPLOADED_PATH>` with the path of `cookies.age`:

```sh
cd ~/fbm-m0 && ~/bin/fbm login --cookies <UPLOADED_PATH>
```

What this does: decrypts in memory with the VM key, validates the cookies against Facebook with one connect,
stores the session state, then deletes `cookies.age`. Allow up to about 100 seconds.

Expected stdout, one line, exit 0:

```json
{"user_id_len":15,"name":"<Karmi's Facebook name>","threads_in_initial_table":20,"socket":"connected"}
```

`user_id_len` and `threads_in_initial_table` are numbers; the exact values vary. `socket` is always
`connected`: login succeeds only when the socket comes up. Any other result prints one `error_class` line,
exits 1, stores no cookies and keeps `cookies.age`.

If it fails:

| Output | Meaning | Action |
|---|---|---|
| `{"error_class":"not_logged_in"}` | Cookies rejected | Re-export and re-encrypt (Export, Encrypt, Deliver), then run login again |
| `{"error_class":"checkpoint"}` or `"challenge"`, `"suspended"`, `"rate_limited"` | Facebook flagged the login | Stop. Open the Facebook app on the phone and clear it. Do not retry. Then see `checkpointed` in the Decision table |
| `{"error_class":"transient"}` or `"unknown"` | Network or protocol hiccup | Wait 10 minutes, run the same command once more. `cookies.age` is only deleted on success |
| `{"error_class":"socket_timeout"}` or `{"error_class":"disconnected"}` | The socket did not come up: no connect within the wait, or 3 failed connects in a row. Cookies were not stored | Stop and report to Karmi. Do not retry and do not move on to the smoke test |
| stderr `fbm login: ...` text, exit 1 | Local problem (key missing, file unreadable, wrong key) | Show the output; do not retry blindly |

## Threads smoke test

Prints only the thread kind, E2EE flag and last-activity time. Names are cut off so correspondents' names
do not enter the transcript:

```sh
cd ~/fbm-m0 && ~/bin/fbm threads --limit 3 --format text | cut -f1-3
```

Allow up to about 100 seconds.

Expected: up to three tab-separated lines such as `dm	false	2026-10-15T09:12:03Z`. Kind is `dm`, `group` or
`other`. E2EE threads show `true`; M0 does not read them, which is expected.

An `{"error_class":...}` line instead (exit 1, no rows) means the session did not come up: see the Login
table. `socket_timeout` or `disconnected` means stop and report to Karmi; do not start the dry run. If it
prints nothing at all with no error, the account has no visible threads; stop and report.

## Dry run first

A short end-to-end rehearsal of the spike path: six minutes on the same IP, same code. This is a real
Facebook connection, so it counts as part of the exposure.

```sh
umask 077; cd ~/fbm-m0 && nohup ~/bin/fbm spike --hours 0.1 > dryrun.out 2>&1 &
```

`umask 077` makes `dryrun.out` (and `spike.out` later) readable by the VM user only.

Wait seven minutes, then ask the Dot to run:

```sh
grep '"outcome"' ~/fbm-m0/dryrun.out && ~/bin/fbm status
```

Expected: the `grep` line is the verdict JSON with `"outcome":"survived"` and `"planned_hours":0.1`, and
`fbm status` shows the same object under `last_verdict`. Anything other than `survived`: stop, read the
Decision table, do not start the 48-hour run. Note that `fbm spike` exit codes are 0 (survived), 3
(checkpointed, logged_out, disconnected), 1 (aborted or other failure), 2 (bad arguments).

## Start spike

Only after the dry run is `survived`. Ask the Dot to run:

```sh
umask 077; cd ~/fbm-m0 && nohup ~/bin/fbm spike --hours 48 > spike.out 2>&1 & echo "pid $!"
```

The Dot reports the pid. Then confirm it is alive:

```sh
ps -C fbm -o pid,etime,args
```

Expected: a header line and one process line whose `args` show `/home/.../bin/fbm spike --hours 48`, with
`ELAPSED` counting up from the start. Only the header line means no `fbm` process is running. (`ps -C fbm`
matches the process name, so it cannot match the wrapper shell the way `pgrep -f` can.) Write down the
start time.

Mid-run checks cost nothing and send nothing to Facebook:

- Same `ps -C fbm` as above: a missing process before the 48 hours are up means the VM or the Dot session killed
  it. That is itself a finding for the design (background processes do not outlive the Dot session), so
  report it. A process killed with SIGTERM writes an `aborted` verdict. One killed with SIGKILL or a VM
  restart writes none, so `fbm status` keeps showing the dry-run verdict.
- `fbm status` shows `last_verdict`. While the spike runs it still holds the previous run's verdict (the
  dry run). Tell the verdicts apart by `planned_hours`: 0.1 is the dry run, 48 is the real one. The real
  verdict appears only when the run ends.

Do not start a second `fbm spike`, `fbm login` or `fbm threads` while it runs: each opens its own connection
to Facebook and breaks the one-session assumption the test depends on.

## Read verdict

At 48 hours after the start, ask the Dot to run:

```sh
~/bin/fbm status && grep '"outcome"' ~/fbm-m0/spike.out
```

`fbm status` stdout looks like this (values vary):

```json
{"cookies":{"present":true,"age_s":172900},"session":{"present":true,"age_s":120,"size":4096},"key":{"present":true},"last_verdict":{"started_at":"...","ended_at":"...","planned_hours":48,"outcome":"survived","fatal_class":"","connected":0,"reconnected":3,"transient_disconnects":2,"tables":40,"longest_gap_s":31.5}}
```

The result is valid only when `last_verdict.planned_hours` is `48`. The Dot must paste the JSON back verbatim:
Karmi reads `outcome`, `fatal_class`, `reconnected`, `transient_disconnects`, `longest_gap_s`.

Before acting on any outcome, look at the phone: open the Facebook app and check for a login alert, a
"confirm it's you" prompt, or a lock notice, and check Security alerts under Accounts Center. Record what you
see next to the verdict.

## Decision table

Outcomes and process exit codes come from `fbm spike`.

| `outcome` | Exit | Meaning | Do next |
|---|---|---|---|
| `survived` | 0 | Connected, and the socket was up at 48 h | Proceed to M1. Record the verdict JSON. |
| `checkpointed` | 3 | Facebook flagged the session (`fatal_class` is `checkpoint`, `challenge`, `suspended` or `rate_limited`) | Clear it from the phone (Facebook app). Do not re-run. Decide residential proxy (`--proxy`) versus Mac-hosted. |
| `logged_out` | 3 | Cookies invalid (`not_logged_in`) | First open the Facebook app on the phone. Any security prompt or lock notice: treat as `checkpointed` and do not log in again. Otherwise re-export cookies from the browser, re-encrypt, re-upload, run `fbm login`, then restart from the Threads smoke test and the dry run before another 48-hour run. Check you did not log out in the browser. |
| `disconnected` | 3 | Socket down at the end, never connected, or 3 failed reconnects in a row | Check facebook.com from the phone for a lock notice. If there is none, re-run once with `fbm spike --hours 1` before deciding. |
| `aborted` | 1 | Interrupted (SIGTERM, Ctrl-C) or an unknown permanent error | Re-run once. |

Notes on reading the verdict:

- A `survived` run with a large `longest_gap_s` or many `transient_disconnects` is still a pass, but note it:
  M1 will need its reconnect budget sized from it.
- The spike resumes the session state that login saved, so its first socket event is `reconnected`, not
  `connected`. A healthy run shows `connected` 0 and `reconnected` 1 or more.
- `fatal_class` empty with `survived` is normal. With `disconnected` it holds the class of the last transient
  disconnect, if any.
- If `fbm status` shows no `last_verdict` (`null`) after 48 hours, the process died without writing one (see
  the mid-run checks). Treat it as a finding, not as `survived`.

Result recording is separate from this runbook: save the final verdict JSON, the phone observation and the
start/end times in `docs/runbooks/m0-result-<date>.md` after the run. That file is the deliverable of M0.
