# fbm — read-only Messenger reader for an agent VM

Date: 2026-10-16. Status: v3, final draft for Karmi's approval. Reviewed by Codex counsel (v1 verdict FLAWED, findings applied) and a builder check (all questions resolved).

## Intent

A single Go binary, `fbm`, that lives on the persistent Linux VM behind a ChatGPT Dot and
lets the Dot read Karmi's Facebook Messenger DMs and group chats as context. The Dot
calls it like a connector: tool-shaped subcommands with JSON output, plus an MCP server
mode exposing the same tools. It performs no user actions on Messenger. "Read-only" is
defined precisely in the section of that name, because a live client cannot be literally
write-free.

Why: no official personal Messenger API exists. Meta's Messenger Platform (overview
updated 2026-09-28) is Page- and Instagram-professional-scoped only: a person must
message the Page first and every permission is `pages_*`. No permission reads a
personal inbox or a group chat the user is in. The official data export is manual and
weeks stale, and Karmi rejected it. The Dot needs live-ish context on demand, so the
tool runs a live client on the VM and accepts the account-lock risk that comes with it.

## Goals

- `fbm threads`, `fbm read`, `fbm search`, `fbm info` answer from a local SQLite mirror
  in milliseconds, with the mirror kept warm by one long-lived connection.
- Same four operations exposed as MCP tools through `fbm serve`.
- Reads end-to-end-encrypted DMs, which are now the Messenger default.
- Survives VM reboots without re-login. Survives Meta protocol churn with a dependency
  bump and rebuild; the recovery plan is in "Upgrade and recovery".
- Nothing secret reaches logs, stdout, or the repo.

## Non-goals

- Sending, reacting, marking read, typing indicators, presence. No code path for any of
  these exists in the binary; the underlying library's send APIs are not wired.
- Instagram. mautrix-meta dropped it in v0.2608.0 and so do we.
- Media download. Attachment metadata (type, filename, size, URL) is stored; bytes are
  not fetched.
- Multi-account.
- A UI.

## Anti-goals (failure even if it works)

- Any correspondent-visible effect: read receipts, delivery receipts that other parties
  can see, typing or presence that others can see. Protocol-level maintenance the server
  requires (transport acks, key uploads, device registration) is permitted and listed in
  "Read-only, defined".
- Triggering an account lock through aggressive backfill or login churn.
- Cookie or session values leaking into Dot transcripts, logs or git.

## Constraints

- Runs headless on a persistent Linux VM (x86_64 assumed; the builder adds arm64 if the
  Dot's computer turns out to be arm). Persistent disk, outbound network. Whether the VM
  allows a background daemon is unverified; the design degrades to on-demand sync if not
  (see "Sync modes").
- The only maintained Messenger protocol implementation is `go.mau.fi/mautrix-meta`
  (v0.2609.0, 2026-09-16, Go ≥ 1.26, AGPL-3.0). `pkg/messagix` is the protocol layer;
  its published API still references Matrix `bridgev2` types in places, so `fbm` depends
  on the whole module and links more than it uses. Acceptable for a personal binary.
  Personal, undistributed use, so AGPL imposes nothing.
- Meta breaks the protocol a few times a year (v0.2608.1 was a hotfix for one). `fbm`
  pins a messagix version and expects to bump it.
- E2EE transport uses WhatsApp's stack (`go.mau.fi/whatsmeow`); the module also pulls
  `libsignal` for the cryptography. Reading E2EE chats means registering `fbm` as a new
  Messenger device on the account, which is visible in the account's device list and may
  trigger a security notification. The E2EE glue (device registration, whatsmeow store,
  history restore) lives in `pkg/connector`, which is coupled to Matrix bridgev2, so
  `fbm` re-implements that glue against `pkg/messagix` and `whatsmeow` directly. That is
  the bulk of the build, which is why E2EE is its own milestone. Encrypted history from before registration is
  restorable only with the six-digit secure-storage PIN, and that restore code
  (`login_history.go`) exists on `main` but not in the v0.2609.0 tag. `fbm` pins a `main`
  revision that has it, or waits for the next release; the builder picks after checking.
- Account lock is a known, open problem (mautrix/meta#44, open since 2024-03): Meta may
  checkpoint with captcha, phone, or password reset. Maintainers recommend 2FA on.

## Decisions made

### Language and dependency
Go. messagix is Go, and a static binary installs on the VM with one `scp`. No Python
wrapper exists.

### Auth
`fbm login --cookies <file>`: reads a JSON or Netscape cookie file exported from Karmi's
own browser, requires `c_user`, `xs`, `datr`, validates with one request, and writes
session state to `$XDG_STATE_HOME/fbm/session.json` at mode 0600. Password login flows
in messagix (`messenger-lite` native-app emulation) are not used. The cookie file is
deleted after import unless `--keep`.

`fbm login --e2ee`: registers the E2EE device and prompts for the PIN to restore history.
PIN is read from a TTY or an inherited file descriptor (`--pin-fd 3`), never from argv
or the environment, which child processes inherit. Default is to register E2EE, because
without it the tool misses most DMs. Without a PIN file the login registers the device
and skips restore; encrypted threads then start at registration time and `info` reports
`history_complete: false` for them. Karmi can skip it with `--no-e2ee`. 2FA on the
Facebook account is an onboarding prerequisite; `fbm doctor` cannot verify it, so the
README states it and `fbm login` asks for a y/N confirmation that it is on.

`fbm logout`: revokes the E2EE device where the protocol allows, deletes session state
and keys, and leaves the mirror unless `--purge`.

### Storage
SQLite at `$XDG_DATA_HOME/fbm/fbm.db`, WAL mode. Tables: `threads` (id, name, kind
dm|group, participant_ids, last_message_ts, is_e2ee), `participants` (id, name),
`messages` (id, thread_id, sender_id, ts, text, attachments_json, reply_to, is_e2ee),
`sync_state` (cursor per sync group, last_sync_ok_at, connection_state, per-thread
coverage: oldest_synced_ts and whether history is complete). Messages carry Meta's own
message ID where the protocol gives one; E2EE messages use the WhatsApp-style ID. Unsend
and disappearing-message expiry purge the text and attachments and leave a tombstone
(id, thread, sender, ts, reason). Edits replace the text in place with no history kept.
The sender chose to withdraw or change it; the mirror honours that. My call, not Karmi's.
Full-text search via FTS5 on `messages.text`.

The whatsmeow device store (identity keys, sessions, prekeys) lives in the same SQLite
file in its own tables, written transactionally with the message rows it decrypted.
`device_registered` is derived from that store, not a separate flag.

### Sync ownership
Exactly one process owns the Facebook connection at a time, enforced with a lock file
under `$XDG_STATE_HOME/fbm/`. Two modes, never both:

- `fbm sync --daemon`: the long-lived owner. Realtime stream into SQLite. Backfill is
  newest-first and demand-driven: recent pages of every thread first, then deeper history
  for threads the Dot has asked about, then the rest, under a global request budget
  (default 1 page per 5 s and at most 200 pages per hour, both connections counted).
  Reconnects use jittered exponential backoff; the bridge's forced refresh every 20 h is
  kept. Read commands and MCP tools request a refresh from the daemon over a Unix socket
  when `--live` is passed; they never open their own connection.
- `fbm sync --once`: for VMs that disallow daemons. Acquires the same lock, connects,
  drains both the Lightspeed cursor and the E2EE socket until both report no pending
  work or a 60 s budget expires, records coverage, disconnects. `--live` on a read
  command runs this when no daemon holds the lock.

A durable circuit breaker stops all connection attempts on a checkpoint, an invalid
session, a PIN-attempt failure, or three consecutive reconnect failures. It stays
tripped until `fbm login` or `fbm sync --reset-breaker` is run by a human; `fbm status`
reports the reason. Automatic retry after a lock signal is the behaviour that turns a
checkpoint into a ban.

### Read-only, defined
"Read-only" means no correspondent-visible action and no user-level account change.
Permitted, because the protocol requires them: transport acknowledgments, key and
prekey uploads, E2EE device registration at login, session refresh. Transport-level
delivery acks on the E2EE socket are in this permitted class: the phone already marks
every message delivered, so a second device acking changes nothing a correspondent
sees. "Seen" is the real gate. Forbidden: sending,
reactions, mark-as-read, read receipts, correspondent-visible delivery receipts, typing,
presence that others can see. Note that even "set presence unavailable" is an outbound
presence update; the builder checks whether sending nothing is cleaner than sending
"unavailable".

Enforcement is by audit, not by omission. Omitting send APIs does not stop the library
from emitting receipts on its own. The builder instruments every outbound task and
stanza on both connections across login, incoming message, history fetch, reconnect and
recovery, and produces an allowlist of what `fbm` emits, classified as transport-only
or correspondent-visible. Anything correspondent-visible that is not required by the
server is removed or stubbed at the messagix or whatsmeow call site; the pinned E2EE
initializer connects with `Passive: false`, which is the first thing to inspect.

Release gate, three parts: (1) the audit allowlist is checked in and reviewed; (2) a
test thread with a second account confirms no "seen" and no "delivered" change after
`fbm read` and after a daemon run across an incoming message; (3) the account's device
list and security notifications are checked after `fbm login --e2ee` and the result is
recorded in the README so Karmi knows what Facebook shows. Finding (2) or (3) fails is
not a bug to patch quietly; it is reported to Karmi because it changes the risk posture.

### Interfaces
CLI (all output JSON by default; `--format text|md` for humans):

```
fbm threads  [--limit N] [--since 7d] [--kind dm|group] [--live]
fbm read     <thread-id|name> [--since 7d] [--limit N] [--before <msg-id>] [--live]
fbm search   "<query>" [--thread ID] [--since 30d] [--limit N]
fbm info     <thread-id|name>
fbm status                 # connection, cursor age, E2EE state, db size; secrets shape-only
fbm doctor                 # cookie validity, Go/lib versions, permissions on state files
fbm login / fbm logout / fbm sync
fbm serve    [--stdio] [--http 127.0.0.1:PORT]
```

Every read result carries a `freshness` object: `last_sync_ok_at`, `connection_state`
(connected, disconnected, breaker_tripped with reason), and for the thread in question
`coverage` (oldest_synced_ts, history_complete, e2ee_synced). The newest message
timestamp is not a freshness signal and is not reported as one; a quiet thread is not a
stale one.

MCP (`fbm serve`): tools `list_threads`, `read_thread`, `search_messages`,
`get_thread_info`, `sync_status`. Same parameters as the CLI flags. Stdio by default.
`--http` binds loopback only; exposing it beyond the VM is Karmi's call and out of scope.
Whether Dots can attach a custom MCP server is unverified; the CLI face is the guaranteed
path since the Dot has a shell on its own computer. The MCP face is cheap and included.

### Lock-risk posture
"Moderate" is accepted risk, not a measured probability; nobody has numbers. Mitigations
built in: one session and one stable device identity; no re-login unless the server
rejects the cookies; the request budget and backoff above; the circuit breaker above;
2FA as a prerequisite; optional `--proxy` for a residential or static egress IP (SOCKS5
or HTTP), because field reports tie locks to datacenter IPs, with no claim that it is
proven protection. `fbm status` reports `breaker_tripped: checkpoint` so the Dot tells
Karmi to clear it from a phone instead of retrying.

### Secret hygiene
Everything `fbm` writes is secret-grade: session cookies, E2EE keys, and the decrypted
mirror itself (the DB, its WAL and FTS tables, and any attachment URLs, which are
bearer-ish). All of it lives under `$XDG_STATE_HOME/fbm/` and `$XDG_DATA_HOME/fbm/`,
directories created 0700, files 0600, process umask 077. Both paths are in `.gitignore`
and the binary refuses to run if either lives inside a git work tree.

Logging: payload logging in messagix and whatsmeow is disabled, not redacted; upstream
E2EE debug paths log whole crypto-auth response bodies, so a cookie regex would not be
enough. `fbm` logs from an allowlist of diagnostic fields only. `fbm status` and
`fbm doctor` print lengths and ages, never values. The PIN is read from a TTY or an
inherited fd and is never written anywhere. Backups of the state directory are Karmi's
responsibility; the README says to encrypt them and names what is inside. Retention:
`fbm prune --older-than 180d` is available and off by default.

### Install
The only channel to the VM is the Dot chat, which is a transcript. The binary is not
secret and may travel that way or be built on the VM from the repo (`go build`; the Dot
has outbound network, and OpenAI's "unrecognized source" confirmation will fire once).
The cookie file is secret and never travels in the clear:

1. On the VM: `fbm login --keygen` writes an `age` X25519 keypair to
   `$XDG_STATE_HOME/fbm/` (0600) and prints only the public key. The public key is safe
   in a transcript.
2. On the Mac: `age -r <pubkey> -o cookies.age cookies.json`, then upload `cookies.age`
   through the chat. Ciphertext in the transcript decrypts only on the VM.
3. On the VM: `fbm login --cookies cookies.age` detects the `age` armor, decrypts with the
   VM key, validates, stores session state, deletes `cookies.age`.

Same path re-runs when Meta invalidates the session. The E2EE PIN, if available, is
delivered the same way (`pin.age`) and `fbm login --e2ee --pin-file pin.age` reads and
shreds it; the PIN is never typed into the chat. `age` is vendored as a Go library
(`filippo.io/age`), so the VM needs no extra tool.

Alternative, if the Dot's own Chromium profile is on the same filesystem: the Dot signs
into facebook.com with a saved password (a Dots feature that keeps the password away from
the model) and `fbm login --from-browser <profile-dir>` lifts `c_user`, `xs`, `datr`
from the profile's cookie store. Unverified that the profile is reachable; builder checks
and keeps it only if it is.

Daemon: `fbm sync --daemon` under a systemd user unit if the VM has systemd, else
`nohup` with a restart loop that respects the circuit breaker. Whether the Dot's
computer keeps a background process alive between Dot sessions is unverified; M0 tests
it, and `--once` is the fallback. A `Makefile` builds linux/amd64 and linux/arm64.

### Upgrade and recovery
Meta breaks the protocol a few times a year and the fix is always an upstream bump.
`fbm` pins the mautrix-meta revision in `go.mod`; the recovery playbook is: bump, build,
`scp`, `fbm doctor`. Cookies and the E2EE device survive an upgrade. If the server
invalidates the session, `fbm login` with fresh cookies reuses the existing device store
so history is not re-registered. If the device itself is revoked, `fbm login --e2ee`
registers again and the PIN restore runs again.

## Milestones

- **M0, login spike (throwaway):** cookies from Karmi's browser, `fbm login`, `fbm
  threads` against plaintext threads only, run from the VM's real egress for 48 h with
  the daemon idle-connected. Purpose: learn whether this account on this IP gets
  checkpointed before anything else is built. A checkpoint here sends the design back to
  the proxy or Mac-hosted options.
- **M1, plaintext read:** storage, sync owner, read commands, MCP face, stanza audit and
  release gate on non-E2EE threads (group chats, mostly).
- **M2, E2EE:** device registration, whatsmeow store, PIN restore, audit extended to the
  second socket, device-list check.
- **M3, hardening:** breaker, budgets, prune, doctor, install unit, upgrade playbook.

## Left to the builder

- Exact messagix wiring: which lightspeed tasks list threads and page history, how
  cursors map to `sync_state`, and how E2EE messages arrive via the whatsmeow client.
- The outbound-stanza audit in "Read-only, defined", which is blocking verification
  before any read path ships.
- Which `main` revision to pin for history restore, and the backup prerequisites on
  the phone side for the PIN restore to find anything.
- MCP SDK choice (official `modelcontextprotocol/go-sdk` preferred if stable).
- Schema details beyond the tables above; migrations.
- Name resolution for `<thread-id|name>` (exact id, then case-insensitive prefix on
  thread name, then participant name; ambiguous match returns candidates).
- Test strategy. Protocol tests cannot run in CI; the builder records a manual test plan
  against Karmi's account and unit-tests storage, search, formatting, and redaction.

## Open questions for Karmi (from the builder check)

1. Delivery path: settled, chat-only. See "Install" (age-encrypted to a VM keypair).
2. PIN: settled. Karmi does not remember it; E2EE is on. Encrypted threads are
   forward-only from device registration. `fbm login --e2ee` skips history restore
   when no PIN file is given. If Karmi recovers or resets the PIN from the phone before
   install, the restore path runs; whether a reset preserves the backup is unverified.
3. Proxy: settled, none. Karmi's Mac cannot host a proxy reachable from the
   Dot. M0 runs on the VM's own egress and decides. If M0 checkpoints, the options are
   a paid residential proxy via `--proxy`, or the Mac-hosted rung, and that is a new
   decision for Karmi, not a builder call.

## Calls I made (check these while reading)

- Go, single static binary, messagix pinned to a `main` revision the builder selects.
- E2EE registration on by default; PIN restore optional.
- Unsend purges text, edits replace in place, tombstones kept.
- Backfill newest-first and demand-driven, 200 pages/hour budget, 90 days default depth.
- Circuit breaker is human-reset only.
- Cookies travel chat-only as `age` ciphertext to a VM-generated key.
- Attachment bytes never fetched; metadata only.
- MCP face is stdio by default, HTTP loopback-only; attaching it to Dots is unverified
  and the CLI face is the guaranteed path.
- Milestone M0 is a throwaway spike whose only job is the lock-risk read on this IP.

## Open questions settled by Karmi during brainstorming

- Runtime: persistent VM, packages and files survive. 
- Consumer: the Dot reads for context only; no actions, no notifications.
- Risk: moderate lock risk accepted for live, connector-like behaviour. Official export
  rejected as manual and stale.
- Client location: on the Dot's VM, not Karmi's Mac.

## Sources

- mautrix-meta releases and changelog: https://github.com/mautrix/meta/releases
- messagix API: https://pkg.go.dev/go.mau.fi/mautrix-meta/pkg/messagix
- Required cookies: https://pkg.go.dev/go.mau.fi/mautrix-meta/pkg/messagix/cookies
- Auth and lock warning: https://docs.mau.fi/bridges/go/meta/authentication.html
- Lock reports: https://github.com/mautrix/meta/issues/44
- E2EE history restore: https://github.com/mautrix/meta/blob/main/pkg/connector/login_history.go
- Dots: https://openai.com/index/introducing-dots/
- Messenger Platform scope: https://developers.facebook.com/docs/messenger-platform/overview
