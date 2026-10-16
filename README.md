# fbm

A read-only Facebook Messenger reader CLI in Go. It runs a live
[messagix](https://github.com/mautrix/meta) client (the library under mautrix-meta) as one session on an
agent's computer and answers tool-shaped commands with JSON, so the agent can read Karmi's Messenger threads
for context. It never sends anything to a conversation.

Status: M0, a login spike. It answers one question before anything else is built: does this account, on the
agent's own IP, survive 48 hours idle-connected without a checkpoint? Reading message bodies is M1.

## Quick start on a ChatGPT Dot

Tell your Dot this one sentence:

> Set up https://github.com/karmiphuc/chatgpt-dots-fb-messenger-cli by following DOTS.md in that repo.

The Dot clones the repo, runs `scripts/install.sh` (Go, the build and a Chromium), opens a Chromium window on
its computer for you to log in to Facebook, then runs the 48-hour spike. Your part is that one login, plus
clearing a checkpoint from your phone if Facebook raises one. [DOTS.md](DOTS.md) is the Dot's manual.

## Read-only, defined

"Read-only" means no correspondent-visible action and no user-level account change. The protocol still needs
some traffic (acknowledgments, key uploads, session refresh). Sending, reactions, mark-as-read, read receipts,
typing indicators and visible presence are forbidden, and enforced by audit of what the client emits. The
full definition is in the [design spec](docs/superpowers/specs/2026-10-16-fbm-design.md).

Lock risk is accepted, not measured. Built in today: one session and one stable device identity (the
persistent browser profile), no re-login while the session works, a breaker that ends the spike after 3
failed reconnects, and a latch that stops every command after a checkpoint until the owner clears it.

## Commands (M0)

| Command | Does |
|---|---|
| `fbm login --browser` | Owner logs in through a Chromium window on this computer; fbm reads only the three session cookies |
| `fbm login --cookies <file>` | Import a cookie export (plain or age-armored); `--keygen` makes the age key |
| `fbm threads [--limit N] [--format json\|text]` | List threads from the initial table |
| `fbm status` | Presence, ages and sizes of state files, plus the last spike verdict. Never values |
| `fbm spike [--hours H]` | Stay idle-connected for H hours (default 48), write a verdict |
| `fbm doctor --env [--launch-check]` | Report what this computer offers fbm, optionally launching Chromium once |

`login`, `threads` and `spike` take `--proxy <url>`. State lives in `$XDG_STATE_HOME/fbm` (else
`~/.local/state/fbm`), mode 0700, and the binary refuses a directory inside a git checkout.

## Build

`make build` (host binary in `dist/fbm`), `make linux-amd64`, `make linux-arm64`, `make test`. Needs Go 1.26
or newer; `scripts/install.sh` fetches it on Linux if missing.

## Docs

- [DOTS.md](DOTS.md): the Dot's operating manual
- Specs: [design](docs/superpowers/specs/2026-10-16-fbm-design.md),
  [autonomous login](docs/superpowers/specs/2026-10-16-fbm-autonomous-login-design.md)
- Plans: [M0](docs/superpowers/plans/2026-10-16-fbm-m0-login-spike.md),
  [plug-and-play](docs/superpowers/plans/2026-10-16-fbm-plug-and-play.md)
- [Manual fallback runbook](docs/runbooks/m0-spike.md): cookies exported on a Mac, for when `login --browser`
  cannot work
- [Progress log](docs/PROGRESS.md)

Before publishing, run `scripts/check-public.sh`: it fails if a tracked file holds a local path, an internal
name or an age secret key. Put any extra strings to block (your username, an employer name), one per line, in
`.check-public.local` at the repo root; it is git-ignored and matched as fixed strings, ignoring case.
