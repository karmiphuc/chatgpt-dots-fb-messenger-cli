# fbm M0 Login Spike Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove, on the Dot VM's own egress IP, whether a cookie-only messagix session on Karmi's account survives 48 hours idle-connected without a checkpoint, and ship the pieces of that spike that M1 keeps (cookie import, age delivery, state dir, client wrapper, error classification).

**Architecture:** One Go module, one binary `fbm`. `internal/auth` turns a browser cookie export (optionally age-encrypted) into `*cookies.Cookies`. `internal/state` owns the 0700 state directory, session persistence and secret-safe logging. `internal/meta` wraps `messagix.Client` with the connect sequence the upstream bridge uses (cached state, else `LoadMessagesPage` then `Connect`), classifies errors, and records connection events. `cmd/fbm` exposes `login`, `threads`, `status`, `spike`. No SQLite, no MCP, no E2EE in M0.

**Tech Stack:** Go 1.26 (toolchain 1.27.1 per upstream), `go.mau.fi/mautrix-meta v0.2609.0` (`pkg/messagix`), `filippo.io/age` (latest tag), `github.com/rs/zerolog` (required by messagix), stdlib `flag`, `encoding/json`, `testing`.

**Spec:** `docs/superpowers/specs/2026-10-16-fbm-design.md`

## Global Constraints

- Go `1.26.0` minimum; `go.mod` carries `go 1.26.0` and `toolchain go1.27.1`.
- Dependency pinned: `go.mau.fi/mautrix-meta v0.2609.0`, with upstream's replace copied verbatim: `replace github.com/imroc/req/v3 => github.com/beeper/req/v3 v3.0.0-20260808092153-100cef0a2fbd`.
- Required cookies, exact names: `xs`, `c_user`, `datr` (`cookies.FBRequiredCookies`). Platform is Facebook only.
- Every file under the state dir is `0600`, the dir is `0700`, process umask `077`. The binary refuses to run if the state dir is inside a git work tree.
- No cookie value, session state, or PIN ever reaches stdout, stderr, logs, or git. Status output prints lengths and ages only.
- No call to any messagix task that sends, reacts, marks read, sets presence or typing. M0 calls only `LoadState`, `LoadMessagesPage`, `Connect`, `Disconnect`, `DumpState`, `GetCurrentAccount`, `IsConnected`, `SetEventHandler`.
- On `httpclient.ErrCheckpointRequired`, `ErrChallengeRequired`, or `ErrAccountSuspended`: stop, record, exit non-zero. Never retry automatically.
- Output is JSON on stdout by default. Human-readable only behind `--format text`.
- Commits: conventional (`feat:`, `fix:`, `chore:`, `docs:`, `test:`).
- M0 is a spike. Protocol code gets manual verification against Karmi's account; pure code gets unit tests.

## Review Focus

1. A cookie export that contains `xs`, `c_user`, `datr` plus dozens of unrelated cookies, some expired: import must keep only Meta cookie names and ignore the rest. Test in Task 2.
2. Netscape `cookies.txt` with `#HttpOnly_.facebook.com` domain prefixes: import must treat the prefixed domain as `.facebook.com`. Test in Task 2.
3. Cached session state older than `messagix.MaxCachedStateAge` (24 h): `LoadState` returns `ErrCachedStateTooOld`; the wrapper must fall through to `LoadMessagesPage`, not fail. Test in Task 5.
4. A checkpoint arriving mid-spike as a `PermanentErrorEvent` rather than a connect error: the spike must stop and write the verdict, not sit in the reconnect loop. Test in Task 7.
5. `fbm login` run twice: second run must replace session state atomically and not leave a decrypted cookie file behind. Test in Task 6.

---

### Task 1: Module scaffold and build

**Files:**
- Create: `go.mod`, `go.sum`, `cmd/fbm/main.go`, `Makefile`, `.gitignore` (modify existing)
- Test: `go build ./...` and `go vet ./...`

**Interfaces:**
- Produces: `cmd/fbm/main.go` with `func main()` dispatching on `os.Args[1]` to `runLogin`, `runThreads`, `runStatus`, `runSpike` (each `func(args []string) int`), defined in later tasks. Until then each returns `2` with `"not implemented"` on stderr.
- Produces: `Makefile` targets `build` (host), `linux-amd64`, `linux-arm64` (CGO_ENABLED=0), `test`.

- [ ] **Step 1: `go mod init github.com/karmiphuc/chatgpt-dots-fb-messenger-cli`, set `go 1.26.0` and `toolchain go1.27.1`, add the pinned require and the replace line from Global Constraints**

- [ ] **Step 2: Write `cmd/fbm/main.go` with the dispatcher and a `usage()` that lists the four subcommands**

- [ ] **Step 3: Write the `Makefile`** with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/fbm-linux-amd64 ./cmd/fbm` and the arm64 twin.

- [ ] **Step 4: Verify**

Run: `go mod tidy && go build ./... && go vet ./... && make linux-amd64`
Expected: no output from vet; `dist/fbm-linux-amd64` exists.

- [ ] **Step 5: Append to `.gitignore`: `dist/`, `*.age`, `*.key`, `cookies.json`, `cookies.txt`**

- [ ] **Step 6: Commit** `chore: scaffold fbm module pinned to mautrix-meta v0.2609.0`

---

### Task 2: Cookie import

**Files:**
- Create: `internal/auth/cookies.go`, `internal/auth/cookies_test.go`, `internal/auth/testdata/chrome.json`, `internal/auth/testdata/netscape.txt`

**Interfaces:**
- Produces: `func ParseCookieExport(r io.Reader) (*cookies.Cookies, error)` in package `auth`. Detects format by first non-space byte: `[` or `{` is JSON, otherwise Netscape. Returns `*cookies.Cookies` with `Platform: types.Facebook` (set the exported field directly) and only `cookies.MetaCookieName` entries set. Returns `ErrMissingCookies` wrapping the names from `GetMissingCookieNames()` when any of `xs`, `c_user`, `datr` is absent.
- Produces: `var ErrMissingCookies = errors.New("missing required cookies")`.
- JSON accepted shapes: an array of `{"name","value","domain"}` objects (Cookie-Editor, EditThisCookie) or an object `{"name": "value"}`.

- [ ] **Step 1: Write fixtures.** `chrome.json`: 12 cookies including `xs`, `c_user`, `datr`, `fr`, `sb` and 7 unrelated names on other domains. `netscape.txt`: header comment, one `#HttpOnly_.facebook.com` line for `xs`, plain `.facebook.com` lines for `c_user` and `datr`, one expired line for `fr`.

- [ ] **Step 2: Write the failing tests**

```go
func TestParseCookieExport_JSONArrayKeepsOnlyMetaNames(t *testing.T) {
	c, err := auth.ParseCookieExport(open(t, "testdata/chrome.json"))
	require.NoError(t, err)
	require.Len(t, c.GetMissingCookieNames(), 0)
	require.Equal(t, int64(100001234567890), c.GetUserID())
	require.Empty(t, c.Get("unrelated_cookie"))
}
func TestParseCookieExport_NetscapeHttpOnlyPrefix(t *testing.T) {
	c, err := auth.ParseCookieExport(open(t, "testdata/netscape.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, c.Get(cookies.FBCookieXS))
}
func TestParseCookieExport_MissingDatr(t *testing.T) {
	_, err := auth.ParseCookieExport(strings.NewReader(`[{"name":"xs","value":"a"},{"name":"c_user","value":"1"}]`))
	require.ErrorIs(t, err, auth.ErrMissingCookies)
	require.Contains(t, err.Error(), "datr")
}
```

Use `github.com/stretchr/testify/require`; add it with `go get`.

- [ ] **Step 3: Run tests, expect FAIL with undefined `auth.ParseCookieExport`**

- [ ] **Step 4: Implement `ParseCookieExport`.** Netscape line format is seven tab-separated fields: domain, flag, path, secure, expiry, name, value. Strip a leading `#HttpOnly_` from the domain. Populate via `c.Set(cookies.MetaCookieName(name), value)` for names in the union of `FBRequiredCookies` and the optional set `sb`, `fr`, `wd`, `presence`, `oo`, `dpr`.

- [ ] **Step 5: Run tests, expect PASS**

- [ ] **Step 6: Commit** `feat(auth): parse Chrome JSON and Netscape cookie exports into messagix cookies`

---

### Task 3: age key and decrypt

**Files:**
- Create: `internal/auth/age.go`, `internal/auth/age_test.go`

**Interfaces:**
- Produces: `func Keygen(dir string) (publicKey string, err error)`: writes `dir/fbm.key` (0600) containing the age X25519 identity, returns the recipient string (`age1...`). Refuses if the file exists (`os.ErrExist`).
- Produces: `func DecryptIfArmored(path string, keyFile string) ([]byte, error)`: if the file starts with `-----BEGIN AGE ENCRYPTED FILE-----` or the binary age header `age-encryption.org/v1`, decrypts with the identity in `keyFile`; otherwise returns the bytes unchanged.
- Consumes: `filippo.io/age` (`age.GenerateX25519Identity`, `age.Encrypt`, `age.Decrypt`, `armor`).

- [ ] **Step 1: Write the failing tests**

```go
func TestKeygenThenDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pub, err := auth.Keygen(dir)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(pub, "age1"))
	st, _ := os.Stat(filepath.Join(dir, "fbm.key"))
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	enc := encryptForTest(t, pub, []byte(`[{"name":"xs","value":"v"}]`)) // armored
	p := filepath.Join(dir, "c.age"); os.WriteFile(p, enc, 0o600)
	got, err := auth.DecryptIfArmored(p, filepath.Join(dir, "fbm.key"))
	require.NoError(t, err)
	require.Equal(t, `[{"name":"xs","value":"v"}]`, string(got))
}
func TestKeygenRefusesOverwrite(t *testing.T) { /* second Keygen → errors.Is(err, os.ErrExist) */ }
func TestDecryptIfArmored_PlainPassthrough(t *testing.T) { /* plain JSON returns unchanged */ }
```

- [ ] **Step 2: Run, expect FAIL**
- [ ] **Step 3: Implement both functions**
- [ ] **Step 4: Run, expect PASS**
- [ ] **Step 5: Commit** `feat(auth): age keypair and decrypt-if-armored for chat-only cookie delivery`

---

### Task 4: State directory, session persistence, safe logging

**Files:**
- Create: `internal/state/store.go`, `internal/state/store_test.go`, `internal/state/log.go`

**Interfaces:**
- Produces: `type Store struct { Dir string }` and `func Open(dir string) (*Store, error)`: `dir` defaults to `$XDG_STATE_HOME/fbm` or `~/.local/state/fbm`; creates it `0700`; calls `syscall.Umask(0o077)`; returns `ErrInsideGitTree` if any ancestor contains `.git`.
- Produces: `func (s *Store) WriteSecret(name string, data []byte) error` (temp file + rename, 0600), `func (s *Store) ReadSecret(name string) ([]byte, error)`, `func (s *Store) Remove(name string) error`, `func (s *Store) Stat(name string) (size int64, age time.Duration, ok bool)`.
- Fixed names used by later tasks: `session.json` (messagix `DumpState` output), `cookies.json` (serialized `*cookies.Cookies` via `MarshalJSON`), `fbm.key`, `status.json`, `spike.log`, `spike-verdict.json`.
- Produces: `func NewLogger(debugProtocol bool) zerolog.Logger` in `log.go`: writes to stderr at Info; when `debugProtocol` is false returns `zerolog.Nop()` for the messagix client. Never attach cookie or state values as fields; the only allowed string fields are `event`, `reason`, `thread_count`, `elapsed`.

- [ ] **Step 1: Write the failing tests**

```go
func TestOpenCreates0700AndRefusesGitTree(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	s, err := state.Open(d); require.NoError(t, err)
	st, _ := os.Stat(d); require.Equal(t, os.FileMode(0o700), st.Mode().Perm())
	g := t.TempDir(); os.Mkdir(filepath.Join(g, ".git"), 0o755)
	_, err = state.Open(filepath.Join(g, "sub", "state")); require.ErrorIs(t, err, state.ErrInsideGitTree)
}
func TestWriteSecretIs0600AndAtomic(t *testing.T) { /* write, stat perm 0600, no *.tmp left */ }
func TestStatReportsShapeOnly(t *testing.T) { /* size and age, never content */ }
```

- [ ] **Step 2: Run, expect FAIL**
- [ ] **Step 3: Implement `store.go` and `log.go`**
- [ ] **Step 4: Run, expect PASS**
- [ ] **Step 5: Commit** `feat(state): 0700 state dir with atomic 0600 secrets and a no-secrets logger`

---

### Task 5: messagix client wrapper and error classification

**Files:**
- Create: `internal/meta/session.go`, `internal/meta/errors.go`, `internal/meta/errors_test.go`, `internal/meta/session_test.go`

**Interfaces:**
- Produces: `type Session struct { Client *messagix.Client; Me types.UserInfo; Initial *table.LSTable; Events chan Event }`.
- Produces: `type Event struct { At time.Time; Kind string; Err error }` with `Kind` in `connected`, `reconnected`, `transient_disconnect`, `permanent_error`, `table`.
- Produces: `func Open(ctx context.Context, st *state.Store, c *cookies.Cookies, proxy string, log zerolog.Logger) (*Session, error)`:
  1. `cli := messagix.NewClient(c, log, &messagix.Config{ClientSettings: exhttp.ClientSettings{ProxyAddress: proxy}})`.
  2. If `st.ReadSecret("session.json")` succeeds and `cli.LoadState(raw)` returns nil, skip to step 4. If it returns `messagix.ErrCachedStateTooOld` or any other error, log `event=state_discarded` and continue.
  3. `me, tbl, err := cli.LoadMessagesPage(ctx)`; on error return `nil, Classify(err)`.
  4. `cli.SetEventHandler(...)` translating `*messagix.ConnectedEvent`, `*messagix.ReconnectedEvent`, `*messagix.TransientDisconnectEvent`, `*messagix.PermanentErrorEvent`, `*table.LSTable` into `Event` on a buffered channel (cap 64).
  5. `cli.Connect(ctx)`; on error return `nil, Classify(err)`.
  6. `raw, _ := cli.DumpState(); st.WriteSecret("session.json", raw)`.
- Produces: `func (s *Session) Close(st *state.Store)`: `DumpState` then `WriteSecret`, then `Disconnect()`.
- Produces in `errors.go`: `type Class string` with constants `ClassCheckpoint`, `ClassChallenge`, `ClassSuspended`, `ClassNotLoggedIn`, `ClassTransient`, `ClassUnknown`; `type ClassifiedError struct { Class Class; Err error }` implementing `error` (message is `string(Class)` only, never `Err.Error()`) and `Unwrap() error`; `func Classify(err error) *ClassifiedError` mapping `httpclient.ErrCheckpointRequired`, `httpclient.ErrChallengeRequired`, `httpclient.ErrAccountSuspended` via `errors.Is`, `websocket.CloseStatus(err) == dgw.CloseStatusUnauthorized` to `ClassNotLoggedIn`, `net.Error` timeouts to `ClassTransient`, else `ClassUnknown`. Passing an existing `*ClassifiedError` returns it unchanged.
- Produces: `func IsFatal(c Class) bool`: true for checkpoint, challenge, suspended, not-logged-in.

- [ ] **Step 1: Write the failing tests for `Classify` and `IsFatal`** using the real sentinel errors wrapped with `fmt.Errorf("x: %w", ...)`; and one test for the `LoadState` fallthrough that constructs a `Store` with a `session.json` containing `{}` and asserts `Open` reaches `LoadMessagesPage` (observable: error is not `ErrCachedStateTooOld`; without network the call fails, assert the class is `ClassTransient` or `ClassUnknown`, never a nil session).

- [ ] **Step 2: Run, expect FAIL**
- [ ] **Step 3: Implement `errors.go`, then `session.go`**
- [ ] **Step 4: Run, expect PASS**
- [ ] **Step 5: Commit** `feat(meta): messagix session wrapper with cached-state fallthrough and error classes`

---

### Task 6: `fbm login`, `fbm threads`, `fbm status`

**Files:**
- Create: `cmd/fbm/login.go`, `cmd/fbm/threads.go`, `cmd/fbm/status.go`, `cmd/fbm/output.go`, `cmd/fbm/output_test.go`
- Modify: `cmd/fbm/main.go` (wire the three)

**Interfaces:**
- Consumes: Task 2 `ParseCookieExport`, Task 3 `Keygen`/`DecryptIfArmored`, Task 4 `Store`, Task 5 `Open`/`Classify`.
- `fbm login --keygen`: `Keygen(st.Dir)`, print `{"public_key": "age1..."}`. Exit 0.
- `fbm login --cookies <path> [--proxy URL] [--keep]`: `DecryptIfArmored` → `ParseCookieExport` → `Open` (which validates by `LoadMessagesPage`) → write `cookies.json` via `c.MarshalJSON()` → `Close`. Delete `<path>` unless `--keep`. Print `{"user_id_len": N, "name": me.GetName(), "threads_in_initial_table": len(Initial.LSDeleteThenInsertThread)}`. Exit 1 on fatal class with `{"error_class": "checkpoint"}`.
- `fbm threads [--limit 50] [--format json|text]`: loads `cookies.json`, `Open`, prints one object per thread from `Initial.LSDeleteThenInsertThread`: `{"thread_key", "name", "kind": "dm"|"group"|"other", "e2ee": bool, "last_activity_ms", "sync_group"}`, sorted by `LastActivityTimestampMs` desc. Then `Close`. Thread name for a DM may be empty in the initial table; print `""`, do not resolve in M0.
- `fbm status`: no network. Prints `{"cookies": {"present": bool, "age_s": n}, "session": {"present": bool, "age_s": n, "size": n}, "key": {"present": bool}, "last_verdict": <contents of spike-verdict.json or null>}`.
- Produces in `output.go`: `func writeJSON(w io.Writer, v any) error` and `func threadKind(t table.ThreadType) string`: `t.IsOneToOne()` → `dm`; `table.GROUP_THREAD`, `table.ENCRYPTED_OVER_WA_GROUP`, `table.TINCAN_GROUP_DISAPPEARING`, `table.CARRIER_MESSAGING_GROUP` → `group`; else `other`. Also emit `"e2ee": t.IsWhatsApp()` on each thread row so the M0 output already shows how many threads M1 can read without E2EE.

- [ ] **Step 1: Write the failing tests** for `threadKind` and for `writeJSON` producing one line, no trailing fields that look like secrets (assert the output of the status object never contains the literal cookie value from a fixture).

- [ ] **Step 2: Run, expect FAIL**
- [ ] **Step 3: Implement the three commands and `output.go`**
- [ ] **Step 4: Run unit tests, expect PASS; `go vet`**
- [ ] **Step 5: Manual verification on the Mac with a fresh cookie export (home IP):**

Run: `./fbm login --cookies ~/Downloads/cookies.json --keep && ./fbm threads --limit 5 --format text && ./fbm status`
Expected: login prints a name and a thread count; threads prints five rows with recent timestamps; status shows cookies and session present and no values. Record the thread count in the commit message body.

- [ ] **Step 6: Re-run `login` and confirm `session.json` mtime changed and no `*.tmp` or decrypted copy remains (Review Focus 5)**

- [ ] **Step 7: Commit** `feat(cli): login, threads and status commands`

---

### Task 7: `fbm spike`

**Files:**
- Create: `cmd/fbm/spike.go`, `internal/meta/spike.go`, `internal/meta/spike_test.go`

**Interfaces:**
- Consumes: Task 5 `Session.Events`, `Classify`, `IsFatal`; Task 4 `Store`.
- Produces: `type Verdict struct { StartedAt, EndedAt time.Time; PlannedHours float64; Outcome string; FatalClass string; Connected, Reconnected, TransientDisconnects, Tables int; LongestGap time.Duration }` with `Outcome` in `survived`, `checkpointed`, `logged_out`, `aborted`.
- Produces: `func RunSpike(ctx context.Context, s *Session, st *state.Store, hours float64, logw io.Writer) (*Verdict, error)`: consumes `s.Events` until the deadline or a fatal event. Each event is appended to `logw` as one JSON line `{"at","kind","reason"}` where `reason` is the error class string, never the error text. On `permanent_error`, `c := Classify(evt.Err).Class`; if `IsFatal(c)`, set `Outcome` accordingly and return immediately (Review Focus 4). Writes `spike-verdict.json` through `st.WriteSecret` (it holds no secret, but the dir policy is uniform) on every exit path including `ctx` cancel (`aborted`).
- `fbm spike --hours 48 [--proxy URL]`: `Open`, `RunSpike` with `spike.log` as `logw`, `Close`, print the verdict as JSON, exit 0 on `survived`, 3 on `checkpointed`/`logged_out`, 1 otherwise.

- [ ] **Step 1: Write the failing test** feeding a fake `Events` channel: three `transient_disconnect` then one `permanent_error` wrapping `httpclient.ErrCheckpointRequired`; assert `Outcome == "checkpointed"`, `TransientDisconnects == 3`, the function returns before the deadline, and the log lines contain `"reason":"checkpoint"` and never the error string.

- [ ] **Step 2: Run, expect FAIL**
- [ ] **Step 3: Implement `RunSpike` and the command**
- [ ] **Step 4: Run, expect PASS**
- [ ] **Step 5: Commit** `feat(spike): 48h idle-connect spike with verdict file`

---

### Task 8: M0 runbook and the VM run

**Files:**
- Create: `README.md`, `docs/runbooks/m0-spike.md`

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Write `docs/runbooks/m0-spike.md`** with these exact sections: Prerequisites (2FA on, confirmed by Karmi), Build (`make linux-amd64`, or on the VM `go install ./cmd/fbm` if Go 1.26+ is present), Deliver binary (chat upload), Keygen on VM, Export cookies on Mac (Cookie-Editor → JSON, facebook.com only), Encrypt (`age -r <pubkey> -a -o cookies.age cookies.json`), Deliver `cookies.age` (chat upload), Login, Threads smoke test, Start spike (`nohup fbm spike --hours 48 > spike.out 2>&1 &`), Read verdict (`fbm status`), Decision table (survived → proceed to M1; checkpointed → clear from phone, then decide proxy vs Mac-hosted; logged_out → cookies invalid, re-export).

- [ ] **Step 2: Write `README.md`**: one paragraph on what fbm is, the read-only definition from the spec, link to the spec and the runbook, and the risk statement verbatim from the spec's "Lock-risk posture".

- [ ] **Step 3: Commit** `docs: M0 runbook and README`

- [ ] **Step 4: Execute the runbook on the Dot VM (Karmi drives the chat uploads).** Record the verdict JSON into `docs/runbooks/m0-result-<date>.md` and commit it. This step is the deliverable of M0; the plan for M1 is written only after it.

---

## After M0: task groups for M1–M3 (expand into plans once the verdict is in)

- **M1 plaintext read:** `internal/store` SQLite schema and migrations (threads, participants, messages, sync_state, FTS5); `internal/sync` single-owner lock file, daemon loop consuming `Events` tables into the store, newest-first backfill via `FetchMoreThreads(ctx, syncGroup)` under the 200 pages/hour budget; `fbm read`, `fbm search`, `fbm info` from the store with the `freshness` object; `fbm serve` stdio MCP with `list_threads`, `read_thread`, `search_messages`, `get_thread_info`, `sync_status`; the outbound-stanza audit and release gate from the spec.
- **M2 E2EE:** `PrepareE2EEClient()` and `RegisterE2EE(ctx, fbid)` wiring, whatsmeow device store in the same SQLite file, optional PIN restore via `securehistory`, audit extended to the WhatsApp socket, device-list check recorded in the README.
- **M3 hardening:** durable circuit breaker, request budgets across both sockets, `fbm prune`, `fbm doctor`, systemd user unit, `fbm logout` with device revocation, upgrade playbook.
