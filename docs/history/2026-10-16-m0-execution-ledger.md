# SDD ledger — plan: docs/superpowers/plans/2026-10-16-fbm-m0-login-spike.md

Spec: docs/superpowers/specs/2026-10-16-fbm-design.md (reachable)
Branch: feature/m0-login-spike (from main 0f64f5d+ec444fd). Go 1.27.2 via brew.

## Pre-flight scan

| Rows | Produces / consumes | Finding |
|---|---|---|
| T1 ↔ T6/T7 | main.go dispatcher runLogin/runThreads/runStatus/runSpike `func([]string) int` | consistent |
| T2 ↔ T6 | ParseCookieExport(io.Reader) (*cookies.Cookies, error) | consistent |
| T3 ↔ T4 | Keygen writes fbm.key with os directly; T4 fixed name fbm.key | consistent; T3 predates Store, fine |
| T3 ↔ T6 | Keygen(dir), DecryptIfArmored(path, keyFile) | consistent |
| T4 ↔ T5/T6/T7 | Store{Dir}, Open, WriteSecret/ReadSecret/Remove/Stat, fixed names | consistent |
| T5 ↔ T6 | Session.Initial consumed by login/threads | CONFLICT R1: cached-state path skips LoadMessagesPage so Initial is nil and `threads` prints nothing |
| T5 ↔ T7 | Session.Events, Classify, IsFatal | consistent; RunSpike must not touch s.Client (test builds Session with nil Client) |
| T5 self | Classify returns *ClassifiedError, Open returns (nil, Classify(err)) | R2: Classify(nil) must return nil and never be returned as typed-nil error |
| T5 self | session test "never a nil session" vs Open returning error | CONFLICT R3: contradictory assertion |
| T5 self | session test relies on "without network the call fails" | CONFLICT R4: test would send a real request to Facebook if network exists |
| T6 self | Step 5-6 manual verification needs Karmi's live cookies | R5 |
| T8 self | Step 4 VM run needs Karmi | R5 |
| T1 self | module path github.com/karmi/fbm | fine, never published (renamed to github.com/karmiphuc/chatgpt-dots-fb-messenger-cli in the plug-and-play plan, Task 1) |
| T4 self | ErrInsideGitTree ancestor walk on non-existent path | fine; ~ and ~/.local are not git trees (checked) |

## Rulings

- Ruling R0: work on branch feature/m0-login-spike in place, no separate worktree — repo is new, only docs on main, branch gives isolation — costs nothing if wrong.
- Ruling R1: `meta.Open` gains a `fresh bool` parameter after `proxy`: `Open(ctx, st, c, proxy string, fresh bool, log)`. fresh=true skips cached state and always calls LoadMessagesPage so `Initial` is populated. login and threads pass true; spike passes false (uses cache like upstream connectWithCache). — spec wants threads to work; plan's cache path can't deliver it — costs one extra page load per `threads` call if wrong.
- Ruling R2: `Classify(nil)` returns nil; callers only call Classify on non-nil errors so no typed-nil error escapes. — Go typed-nil pitfall — costs nothing.
- Ruling R3: session test asserts err != nil, class ∈ {ClassTransient, ClassUnknown}, and !errors.Is(err, messagix.ErrCachedStateTooOld); drops "never a nil session". — contradiction in plan — costs nothing.
- Ruling R4: session test passes proxy "http://127.0.0.1:1" so every request fails locally; no test may reach facebook.com. — spec anti-goal on lock risk + deterministic tests — costs nothing.
- Ruling R5: manual steps needing Karmi's account (T6 steps 5-6, T8 step 4) are not run by implementers; they are listed as pending for Karmi at hand-off. — subagents have no cookies and must not touch the account — login path is unproven until Karmi runs it.

## Progress
Task 1: dispatched (BASE ec444fd, implementer sonnet)
- Ruling R6: commit trailer names the model that wrote the commit (subagent's own attribution), not the controller's — attribution should be truthful — costs nothing.
Task 1: implementer DONE 6f3d32e; review dispatched (sonnet). Implementer agent ae24c37b5f89d7eb7
Task 1: minor (deferred): go build ./... leaves ./fbm in repo root; add /fbm to .gitignore
Task 1: ⚠️ build/vet verified by controller (go build + go vet clean); trailer resolved by R6
Task 1: minor (deferred): .superpowers/ folded into scaffold commit; cookies.json redundant with cookies*.json; help writes usage to stderr on exit 0; Makefile build lacks mkdir -p dist
Task 1: complete (commits ec444fd..6f3d32e, review clean)
Task 2: dispatched (BASE 6f3d32e, implementer sonnet)
Task 2: implementer DONE 438e011,27d3404; agent ad9ad65f9a1b3aa15
Task 2: review dispatched (sonnet)
Task 2: review Spec ✅ quality Approved; ⚠️ Review Focus 1 (expired + unrelated cookies) not exercised
- Ruling R7: ⚠️ confirmed as a real gap. Review Focus 1 + reviewer minor 1 (domain) + minor 3 (object shape untested) enter fix round 1 together: keep only cookies whose domain is facebook.com or *.facebook.com when a domain is present; skip entries whose expiry is set and in the past; tests for both plus the object-shape JSON and c_user/datr in the Netscape test. — the runbook tells Karmi to export cookies, and a multi-site export with a messenger.com or instagram.com datr silently overwriting the facebook.com one is a login failure he cannot diagnose — costs ~20 lines if wrong.
Task 2: minor (deferred): non-string JSON value fails whole parse; test import grouping
Task 2: fix round 1 implementer DONE f5eb1f5; scoped re-review dispatched (sonnet)
Task 2: fix round 1/5 (3 addressed, 0 open; commits 27d3404..f5eb1f5)
Task 2: minor (deferred): duplicate names across facebook.com subdomains resolve last-wins; restore() called twice in one test
Task 2: complete (commits 6f3d32e..f5eb1f5, review clean)
Task 3: dispatched (BASE f5eb1f5, implementer sonnet)
Task 3: implementer DONE d817f88; agent a46e62e6fc67d6e71; review dispatched (sonnet)
Task 3: ⚠️ replace line verified present in go.mod by controller
Task 3: minor (deferred): ignored os.Remove on cleanup uncommented; key file perms not checked on load; weak leak assertion in wrong-key test
Task 3: complete (commits f5eb1f5..d817f88, review clean)
Task 4: dispatched (BASE d817f88, implementer sonnet)
- Ruling R8: Task 4 splits the plan's single NewLogger into NewLogger (fbm's own, stderr Info) and ProtocolLogger(debug) (Nop unless debug) for messagix — the plan's one function had two contradictory jobs; spec requires payload logging off — costs one extra exported func if wrong.
Task 4: implementer DONE dc756aa; agent abb1301137f6d911e; review dispatched (sonnet)
- Ruling R9: Classify also maps httpclient.ErrTokenInvalidated (which wraps ErrTokenInvalidatedRedirect and ErrUserIDIsZero) to ClassNotLoggedIn, and httpclient.ErrConsentRequired to ClassChallenge — upstream treats both as human-action states; plan's list was incomplete — costs nothing.
- Ruling R10: messagix Connect(ctx) runs its reconnect loop under ctx, so meta.Open's ctx must be the session lifetime, not a short timeout; document on Open — upstream client.go:233-250 — a short ctx would silently kill the connection.
- Ruling R11: the event handler calls cli.PostHandlePublishResponse(tbl) for every non-initial *table.LSTable after queueing it, as upstream handlemeta.go does; it only updates local sync cursors (socket.go:163-178) — without it cursors go stale — costs nothing.
Task 4: ⚠️ ProtocolLogger(true) can write response bodies to stderr — Ruling R12: M0 always passes ProtocolLogger(false); no flag exposes debug in M0 — spec forbids payload logging — costs debugging convenience only.
Task 4: minor (deferred): NewLogger param unused; symlink git-tree path untested; no parent dir fsync after rename; stale .tmp secrets not swept; Stat age may be negative under skew
Task 4: complete (commits d817f88..dc756aa, review clean)
Task 5: dispatched (BASE dc756aa, implementer sonnet)
Task 5: implementer DONE_WITH_CONCERNS 0b5272a; agent a104862cffc8fb6b8
INCIDENT: during Task 5 TDD, 3-4 session-test requests went direct to www.facebook.com from this Mac with the fake fixture cookies (a fake 15-digit c_user, fake xs). Cause: plan/R4 assumed ClientSettings.ProxyAddress routes traffic; in messagix only WithProxy sets the transport. Karmi's account and cookies were never involved. Fixed in 0b5272a with WithProxy wiring plus an offline transport test and a refusing-proxy test. Report to Karmi at hand-off.
- Ruling R13: when `fbm spike` opens with fresh=false and Open fails with a non-fatal class, retry once with fresh=true; fatal classes stop immediately — implementer concern 4 — costs one extra page load on a stale cache.
Task 5: review dispatched (opus — core protocol code)
Task 5: review (opus) Spec ✅ with accepted deviations; Needs fixes: 2 Important (both plan-mandated), 5 Minor
Task 5: ⚠️ incident already ledgered above
- Ruling R14: Close calls Disconnect() first, then saves state — plan order (dump before disconnect) races SyncManager.store against the live handler; upstream disconnects first (connector/client.go:565-568) — costs nothing.
- Ruling R15: Open no longer saves state; the handler records a permanent_error, and Close skips the save and removes session.json when one occurred — re-stamping defeated MaxCachedStateAge and resurrected a known-bad state on every spike run — costs one fresh page load after a 4003.
- Ruling R16: httpclient.ErrRateLimited and ErrTooManyRedirects map to a new fatal ClassRateLimited — spec: automatic retry after a lock signal is what turns a checkpoint into a ban; upstream treats both as permanent — costs a manual restart after a transient 429.
- Ruling R17: the handler test client uses clientSettings("http://127.0.0.1:1") — hard rule that no test reaches Meta; defence in depth — costs nothing.
- Ruling R18: Open success does not mean the socket connected (Connect is async). Tasks 6 and 7 wait for the first connected or permanent_error event (login/threads up to 30s) and treat permanent_error by its class — carried into those dispatches.
Task 5: minor (deferred): CONNECT-prefix assertion order in session_test.go:109; load_panic test depends on upstream bug (accept load_failed too); no fresh fallback when cached state loads but is unusable (covered by R13 at spike layer)
Task 5: fix round 1 implementer DONE 17351eb; scoped re-review dispatched (sonnet)
Task 5: fix round 1/5 (4 addressed, 0 open; commits 0b5272a..17351eb)
Task 5: minor (deferred): two Close tests build client with messagix.Config{} not dead proxy (offline today); Disconnect 5s timeout leaves theoretical race; brief text for Close order now stale (superseded by R14)
Task 5: complete (commits dc756aa..17351eb, review clean)
Task 6: dispatched (BASE 17351eb, implementer sonnet)
- Ruling R19: Task 6 login/threads wait up to 30s for connected/permanent_error after Open; timeout proceeds with Initial data and reports socket:"timeout"; cookies.json written only after success — R18 applied; spec: tool must report checkpoint instead of silently succeeding — costs up to 30s per command.
Task 6: implementer DONE_WITH_CONCERNS fb5e495; agent a3aaf6edb2142e659
- Ruling R20: `threads` reports socket state on stderr (socket_connected/socket_timeout), not stdout, since stdout is one JSON object per thread — keeps the brief's row shape for the Dot — costs the Dot not seeing a socket timeout unless it reads stderr.
- Ruling R21: thread_key is emitted as a JSON string — ids exceed 2^53 and JSON consumers would corrupt them — costs nothing.
Task 6: review dispatched (sonnet)
Task 6: review Spec ✅ Approved; ⚠️ verdict secrecy → carried into Task 7; ⚠️ live steps → R5 pending Karmi; ⚠️ file modes → covered by Task 4 review
- Ruling R23: `--proxy` on `threads` is accepted scope — spec lists --proxy as a lock-risk mitigation and the VM use case needs it on every connecting command — costs nothing.
Task 6: minor (deferred): post-Open ordering in login untested beyond Open-error; user_id_len counts bytes not digits; failed session save still reports success; stdout Flush error swallowed (broken pipe exits 0); local failures print no JSON; verdict echoed uncapped; second Ctrl-C ignored during Close
Task 6: complete (commits 17351eb..fb5e495, review clean)
- Ruling R22: in `fbm spike`, ANY permanent_error ends the run, because messagix stops reconnecting after one. Outcome mapping: not_logged_in → logged_out; checkpoint, challenge, suspended, rate_limited → checkpointed; any other class → aborted. fatal_class always carries the class. longest_gap is the longest interval from a transient_disconnect to the next connected/reconnected, encoded as `longest_gap_s` (float seconds). Verdict JSON uses snake_case tags and holds counts, timestamps and class strings only — no ids, names, or error text. — plan left these undefined; a non-fatal permanent error would otherwise report "survived" on a dead socket — costs a re-run if the mapping is wrong.
Task 7: dispatched (BASE c89d32b, implementer sonnet)
Task 7: implementer DONE ff2a20a; agent ae35cfed43edaf5f0
- Ruling R24: accept --hours <= 8760 and NaN/Inf rejection (exit 2) beyond R22's > 0 — prevents Duration overflow — costs nothing.
Task 7: review dispatched (opus — verdict decides project continuation)
Task 7: review (opus) ❌ named risk 1; Critical 1 (survived on dead socket), Important 2 (deadline race drops permanent_error; fatal class on transient ignored), 6 Minor
Task 7: ⚠️ main.go routing, writeJSON/loadCookies secrecy, verdictFile const — controller to verify after fix round
- Ruling R25: add outcome `disconnected` (exit 3). It is reported when (a) the deadline arrives with Connected+Reconnected == 0 or with a gap open, or (b) 3 consecutive transient_disconnect events arrive with no connected/reconnected between them; (b) ends the run immediately and fatal_class carries the last transient class. — upstream retries non-4003 failures forever every 5 min, which is both a false "survived" and the repeated-login pattern the spec's breaker (3 consecutive reconnect failures) exists to stop — costs a false alarm if a long network outage hits the VM.
- Ruling R26: on deadline/ctx exit, drain Events non-blocking through the same handling and check s.permanent before finalising; a transient_disconnect whose class IsFatal ends the run like a permanent_error — reviewer Important 1 and 2; global constraint "never retry automatically" — costs nothing.
Task 7: minor (deferred): OpenFailedVerdict ended_at==started_at; RunSpike exported guard accepts NaN/Inf; finishSpike nil guard; no unit test for R13 retry count; status.go duplicates VerdictFile const; open-gap test timing-dependent
Task 7: ⚠️ verified by controller: main.go routes spike → runSpike; both verdict constants are spike-verdict.json; writeJSON/loadCookies secrecy already reviewed clean in Task 6
Task 7: fix round 1 implementer DONE 97e0846 (brief's checkpoint test sequence adjusted for R25 breaker); scoped re-review dispatched (opus)
Task 7: fix round 1/5 (3 addressed, 0 open; commits ff2a20a..97e0846)
Task 7: minor (deferred): fatal transient does not set s.permanent so Close saves state the permanent path would delete (one-line fix); disconnected after nil-error transients has empty fatal_class; breaker exit does not drain (test-only reachable); queued-permanent test exercises ctx branch not deadline
Task 7: complete (commits c89d32b..97e0846, review clean)
Task 8: dispatched (BASE 97e0846+progress commit, implementer sonnet)
Task 8: implementer DONE_WITH_CONCERNS abe4efa; agent a0b1d73a1e72ee88c
- Ruling R27: accept clipboard-first encrypt (`pbpaste | age ...`) with the file route as fallback, and the two-line note under the verbatim lock-risk paragraph — plaintext cookies never touch Downloads; note prevents Karmi looking for breaker_tripped in M0 — costs nothing.
Task 8: review dispatched (sonnet)
Task 8: review Spec ✅ Approved with 1 Important (pgrep -f self-match shows dead spike as alive)
- Ruling R28: reviewer Minor "pbcopy clear only runs if age succeeds" is promoted into the fix round — plaintext cookies left on the clipboard on failure breaks the user's secret-hygiene non-negotiable — costs one character.
Task 8: minor (deferred): $! is subshell pid; aborted row omits Open-failure and closed-channel paths; logged_out row lacks re-run spike; re-keygen sentence unanswerable by the Dot; runbook Build lacks Go 1.26 prereq; README read-only wording 4 sentences and asserts audit M0 lacks
Task 8: fix round 1 implementer DONE 916167f; scoped re-review dispatched (sonnet)
Task 8: fix round 1/5 (2 addressed, 0 open; commits abe4efa..916167f)
Task 8: minor (deferred): ps -C line not labelled VM-only; ps -C lists concurrent fbm processes
Task 8: complete (commits 4f48688..916167f, review clean)
Final review: dispatched (opus, range ec444fd..916167f)
Final review (opus): With fixes. C1 (upstream global zerolog logs message content to stderr), I1 (login/threads no transient breaker; timeout treated as success), I2 (re-login keeps stale session.json), I3 (runbook logged_out row may re-login after a checkpoint; example verdict connected/reconnected wrong), I4 (rotated cookies not saved after threads/spike); minors triaged.
- Ruling R29: C1 accepted — zlog.Logger = zerolog.Nop() at the top of main(), with a test; runbook nohup lines get `umask 077;` — confirmed repro; spec says payload logging disabled — costs nothing.
- Ruling R30 (supersedes R19 and R20): waitSocket treats a fatal-class transient as a permanent error and returns "disconnected" after 3 consecutive transients. login succeeds only on "connected": otherwise it prints the error_class (or "socket_timeout" / "disconnected"), exits 1, writes no cookies.json and keeps the export. threads on timeout or disconnected prints no rows, prints the error_class JSON and exits 1. Runbook: timeout or disconnected means stop and report — a flagged account must not get repeated auth attempts — costs a re-run when the socket is merely slow.
- Ruling R31: login --cookies removes session.json before meta.Open — new cookies invalidate the cache — costs one fresh load.
- Ruling R32: runbook logged_out row says check the phone first and treat any security prompt as checkpointed; after a fresh login restart from the threads smoke test and dry run; example verdict shows reconnected on the cached path — reviewer I3 — costs nothing.
- Ruling R33: threads and spike call writeCookies after a Close that saw no permanent error — rotated cookies otherwise drift from session.json — costs one file write.
- Ruling R34 (supersedes R13): drop the spike's fresh-retry on a non-fatal Open failure — on the cached path Connect is async so R13 only ever re-ran a failed page load immediately, which is an automatic retry — costs a manual re-run.
- Ruling R35: a fatal-class transient and the 3-in-a-row breaker both set s.permanent, so Close drops session.json — reviewer minor, promoted — costs one fresh load.
- Ruling R36: runbook adds Go 1.26+ prereq for the Mac (and that toolchain auto-download may be blocked on a managed Mac), and "up to about 100 seconds" for login/threads — reviewer triage — costs nothing.
Final fix wave: dispatched (opus, BASE 21e5038)
Final fix wave: DONE_WITH_CONCERNS 21e5038..3328874 (7 commits); scoped re-review dispatched (opus)
Final fix wave re-review (opus): all findings ADDRESSED, no new Critical/Important.
Final: parked — login non-connected result can save a synced session.json beside old cookies.json in a narrow timer race — Ruling R37: real but parked; the runbook already says stop on socket_timeout and the next login removes session.json first (R31) — costs one confusing spike run if both the race and an ignored instruction happen.
Final: parked — login writes cookies.json before deferred Close while the socket is live; upstream Cookies.MarshalJSON reads without the lock — Ruling R38: real, pre-existing, low likelihood; fix in M1 by writing after Disconnect like closeSession — costs a rare map-race panic during login.
Final: minor (deferred to M1): interrupt prints to stderr only, runbook wording says one error_class line; R34 has no behavioural test
FINAL: M0 complete (ec444fd..3328874), review clean with 2 parked.
