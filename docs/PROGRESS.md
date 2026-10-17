# fbm — progress log

Last updated: 2026-10-17. Branch `feature/m0-login-spike`, nothing pushed anywhere.

## What fbm is

A read-only Facebook Messenger reader CLI in Go for Karmi's ChatGPT Dot VM. It runs a live
messagix client (from mautrix-meta) and answers tool-shaped commands with JSON. It sends
nothing to Messenger.

- Design (approved): `docs/superpowers/specs/2026-10-16-fbm-design.md`
- M0 plan (approved): `docs/superpowers/plans/2026-10-16-fbm-m0-login-spike.md`

## Decisions Karmi made

| Question | Answer |
|---|---|
| What the Dot does with messages | Reads them for context only |
| Runtime | Persistent Linux VM; files and packages survive |
| Official data export | Rejected: manual and stale |
| Official Graph API | Rejected: Page-scoped only, cannot read a personal inbox or group chats |
| Where the live client runs | On the Dot VM; moderate account-lock risk accepted |
| Channel to the VM | Chat upload only; cookies travel age-encrypted to a VM-generated key |
| Messenger secure-storage PIN | Not remembered; E2EE threads will be forward-only from registration |
| Egress proxy | None; Karmi's Mac cannot host a proxy reachable from the Dot; M0 tests the VM's own IP |
| Execution | Subagent-driven, a reviewer per task |

## M0 task status

| Task | Status | Commits |
|---|---|---|
| 1 Module scaffold | Done, reviewed | 6f3d32e |
| 2 Cookie import | Done, reviewed after 1 fix round | 438e011, 27d3404, f5eb1f5 |
| 3 age key and decrypt | Done, reviewed | d817f88 |
| 4 State dir and safe logging | Done, reviewed | dc756aa |
| 5 messagix session wrapper | Done, reviewed after 1 fix round | 0b5272a, 17351eb |
| 6 login, threads, status commands | Done, reviewed | fb5e495 |
| 7 `fbm spike` 48h run | Done, reviewed after 1 fix round | ff2a20a, 97e0846 |
| 8 Runbook and README | Done, reviewed after 1 fix round | abe4efa, 916167f |
| Final whole-branch review | Done: 1 leak + 4 Important fixed in one wave, re-reviewed clean, 2 parked | a1bad52..3328874 |

## Waiting on Karmi

1. After Task 6: run `fbm login` and `fbm threads` once on the Mac with a fresh cookie
   export. This is the first contact with the real account. Exact commands will be in the
   Task 6 report.
2. After Task 8: run the M0 runbook on the Dot VM, which includes 48 hours idle-connected,
   and record the verdict.
3. Later: upload the repo to GitHub through the browser, as Karmi asked on 2026-10-16.
   Before uploading, confirm no secret files are in the tree. `.gitignore` covers `*.age`,
   `*.key`, `cookies*.json` and `session.json`. Test fixtures hold only fake values.

## Incident

During Task 5's TDD, 3–4 session-test requests went straight to www.facebook.com from this
Mac. They carried only the fake fixture cookies, never Karmi's. The plan assumed
`ClientSettings.ProxyAddress` routes traffic, but messagix only honours `WithProxy`. Fixed in
0b5272a, which also added an offline transport test and a refusing-proxy test. The Opus
reviewer verified that no test in the package can reach a Meta host.

## Rulings made during execution

Each ruling lists its cost if wrong.

- **R1:** `meta.Open` takes `fresh bool`. `login` and `threads` force a fresh page load.
  The spike uses the cached state. Cost if wrong: one extra page load per `threads` call.
- **R4/R17:** every test that builds a messagix client uses a dead local proxy. Cost: none.
- **R5:** steps that need Karmi's live account are never run by subagents. Cost: the login
  path stays unproven until Karmi runs it.
- **R7:** cookie import keeps only `facebook.com` cookies and drops expired ones. Cost: about
  20 lines.
- **R8/R12:** messagix always gets a no-op logger, because upstream debug output can include
  whole response bodies. Cost: debugging convenience.
- **R9/R16:** errors for token invalidated, consent required, 429 and too-many-redirects map
  to fatal classes. fbm never auto-retries on them. Cost: a manual restart after a transient
  429.
- **R10:** the session context lives as long as the session. Cost: none.
- **R13:** if the spike's cached state fails on a non-fatal error, it retries once with a
  fresh load. Cost: one extra page load.
- **R14:** `Close` disconnects before saving state, which fixes a data race. Cost: none.
- **R15:** after a permanent error, `Close` deletes `session.json` instead of saving it. Cost:
  one fresh load after a 4003.
- **R18/R19:** a successful `Open` does not mean the socket connected. `login` and `threads`
  wait up to 30 seconds for connected or permanent_error. Cost: up to 30 seconds per command.

- **R22/R25/R26:** any permanent error ends the spike. A new `disconnected` outcome is used
  when the socket is down at the deadline, never connected, or fails 3 reconnects in a row.
  A fatal class on a transient drop also stops the run. Before this fix, the spike could
  report `survived` while Facebook kept refusing the socket. Cost: a false alarm if the VM
  has a long network outage.
- **R24:** `--hours` must be greater than 0 and at most 8760. Cost: none.

## Final review outcome

The Opus final whole-branch review ran across all tasks. It found one confirmed leak and
four Important issues, all fixed in commits a1bad52..3328874 and re-reviewed clean.

- **Leak:** messagix logs through zerolog's global logger, which by default writes sender
  names and message text to stderr. fbm now silences it at startup.
- **Login and threads fail closed:** they succeed only on a connected socket. A timeout,
  3 transient drops in a row, or a fatal-class drop exits 1. Login keeps the export and
  writes no `cookies.json` in that case.
- **Stale state:** re-login drops the old `session.json`. `threads` and `spike` save
  rotated cookies after a clean close.
- **Runbook:** for a `logged_out` result, check the phone first. Any security prompt counts
  as a checkpoint. The example verdict now shows `reconnected` on the cached path.
- **Retry removed:** the spike makes one open attempt and never retries. R34 supersedes
  R13.

Two items are parked for M1 (R37, R38). Both are narrow races in `login`.

The full execution ledger, with every dispatch, review, fix round and ruling, is in
`docs/history/2026-10-16-m0-execution-ledger.md`.

## Autonomous login (next design, awaiting approval)

On 2026-10-16 Karmi asked for the Dot to run fbm with no Mac step. He will log in once in a
browser on the Dot's computer. The design addendum is
`docs/superpowers/specs/2026-10-16-fbm-autonomous-login-design.md`, now at v2.

- **Recommended path:** fbm launches its own Chromium only for the login window and reads
  `c_user`, `xs` and `datr` over a private CDP pipe. It closes the browser before messagix
  starts.
- **Isolation:** advisory in this milestone (DOTS.md rule 8). fbm runs as the Dot's own Unix
  user, so only the Dot's rules and fbm never printing a cookie protect the session. A
  separate Unix user is deferred until probe facts from the real Dot exist.
- **No encryption at rest:** the key would sit on the same VM.
- **Codex counsel:** v1 was judged FLAWED. Its main point was that CDP over a TCP port is
  readable by the Dot's own user. v2 applies every finding.
- **Next step:** Karmi runs probe P1 on the Dot. It decides path B, A or C, and whether
  isolation can be real.

## Plug-and-play on Dots (built, awaiting Karmi)

On 2026-10-16 Karmi asked for the project to be plug-and-play on the Dot. The Dot installs,
logs in and runs everything itself, and Karmi's only action is the Facebook login in the
Dot's browser window. It is published as a standalone public repo at
github.com/karmiphuc/chatgpt-dots-fb-messenger-cli, uploaded through the browser, not git.

- **Plan:** `docs/superpowers/plans/2026-10-16-fbm-plug-and-play.md`. Revisions after Codex
  counsel are binding (6226ede).
- **Deferred:** Unix-user isolation (reported as advisory), path A, and message reading
  (M1).

| Task | Status | Commits |
|---|---|---|
| 1 Module rename and scrub | Done, reviewed | 81f1445 |
| 2 CDP-pipe Chromium launcher | Done, reviewed | 03819a2..ecb1f84 |
| 3 `login --browser`, checkpoint gate | Done, reviewed | 95db094..afc98d8 |
| 4 `doctor --env --launch-check` | Done, reviewed | 2cc0827..40b4eca |
| 5 `install.sh`, DOTS.md, docs | Done, reviewed after 1 fix round | c6adc6b..62d6551 |
| Final whole-branch review | 3 Important + 7 minors fixed in one wave; re-review pending | 34f8f3b..1af4d3c, this commit |

The final fix wave: browser login accepts only the facebook.com home feed (`/` or `/home.php`),
so an unknown 2FA or checkpoint page keeps waiting; the fbm profile has password saving off and
uses `--password-store=basic`; on Linux the kernel kills Chromium if fbm dies; DOTS.md runs the
1-hour spike in the background, stops on any install `[fail]`, and tells Karmi to choose Never on
a save-password offer.

Real Linux check (2026-10-17, Debian 12 arm64 container): as root, install.sh installed Debian's
`chromium` with apt; as a non-root user with no sudo, it installed Chrome for Testing. Both
reported `launch_ok` true.

What Karmi does now: give his Dot one sentence, then log in once in the Chromium window the Dot
opens on its computer. He clears a checkpoint from his phone only if Facebook raises one.

> Set up https://github.com/karmiphuc/chatgpt-dots-fb-messenger-cli by following DOTS.md in that repo.

Still open: upload the tracked tree to the public repo through the browser (run
`scripts/check-public.sh` first). The Dot then runs the 48-hour spike, and its verdict decides M1.

## Leak guardrails (2026-10-17)

The audit was clean: no tokens, cookies, keys, chat content, local paths or private strings
appeared in any of the 19 published commits or the 7 issues. Since then:

- Layered guardrails:
  - credential starvation on the Dot
  - an `export-public.sh` publish gate
  - `check-public.sh`
  - `.gitleaks.toml`
  - git hooks
  - a `leak-guard` CI workflow that uses no secrets
- GitHub hardening:
  - a ruleset that blocks force-push and deletion
  - Actions restricted to `actions/checkout`, pinned by SHA
  - approval required for fork pull requests
  - Wiki off
- Designed with Fable counsel, reviewed by Codex and Fable, and fixed over two rounds. The
  planted-leak suite ends at 423/423.
- Published and verified at 75/75 blobs. The first `leak-guard` run on GitHub passed.
- Open item only Karmi can act on: the ChatGPT Codex Connector has write access to all repos.

Details and reproduction steps: `docs/leak-guardrails.md`. Publishing steps:
`docs/runbooks/publish.md`.

## How to resume if this session is lost

- The execution ledger is committed at `docs/history/2026-10-16-m0-execution-ledger.md`.
- `git log main..feature/m0-login-spike` shows what has landed.
- Continue at the first task in the status table that is not done. Use the plan file and
  the superpowers subagent-driven-development skill.
