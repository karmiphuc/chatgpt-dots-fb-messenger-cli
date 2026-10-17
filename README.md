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

The Dot clones the repo, runs `bash scripts/install.sh` (Go, the build and a Chromium), opens a Chromium window on
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
or newer; `bash scripts/install.sh` fetches it on Linux if missing.

## Docs

- [DOTS.md](DOTS.md): the Dot's operating manual
- Specs: [design](docs/superpowers/specs/2026-10-16-fbm-design.md),
  [autonomous login](docs/superpowers/specs/2026-10-16-fbm-autonomous-login-design.md)
- Plans: [M0](docs/superpowers/plans/2026-10-16-fbm-m0-login-spike.md),
  [plug-and-play](docs/superpowers/plans/2026-10-16-fbm-plug-and-play.md)
- [Manual fallback runbook](docs/runbooks/m0-spike.md): cookies exported on a Mac, for when `login --browser`
  cannot work
- [Progress log](docs/PROGRESS.md)

## Before publishing

This repo is public, and fbm sits next to a Messenger session. Upload only from a folder made by
`bash scripts/export-public.sh`, never from the clone (which also holds fbm state, build output and
`.check-public.local`). Run every script with `bash`: a web upload drops the executable bit.

```sh
bash scripts/export-public.sh            # or --out DIR; --dry-run runs the checks only
```

It refuses uncommitted changes to tracked files, runs `bash scripts/check-public.sh`, copies exactly the
current commit (`git archive HEAD`) to `~/fbm-publish/<UTC timestamp>/`, scans that folder again, and prints
the folder and the commit sha. Upload the folder's contents, dot-files included (`.github`, `.gitignore`,
`.gitleaks.toml`; Cmd+Shift+. shows them in Finder).

`bash scripts/check-public.sh` fails on a local path, an internal name, an age key, a Facebook cookie (`xs`,
`c_user`, `datr`, `fb_dtsg`), fbm output (JSON or text), a Messenger data export (a `messages/` folder outside
`internal/`, `message_N.json` or `.html`), a file named like fbm state or `.check-public.local`, a browser
profile, an archive or a database (extensions in any case), a binary other than a png, jpg, gif, ico or webp
image, a Git LFS pointer, a symlink or a submodule, any JSON outside `internal/*/testdata/`, `.github/` and
`.vscode/`, or any file over 200 KB but `go.sum`. It also runs gitleaks with [`.gitleaks.toml`](.gitleaks.toml)
when gitleaks is installed. Each hit prints as path, line and rule, never the matched text (`--show` adds the
line for the built-in local-path pattern only).

Put extra strings to block (your username, an employer name), one per line, in `.check-public.local` at the
repo root. It is git-ignored, matched as fixed strings ignoring case, and a hit on one prints its path and line
only. The list stays on your machine: the hooks, `bash scripts/export-public.sh` and manual runs use it, and
the `leak-guard` workflow does not, because anyone who can push could edit the workflow to print a secret it
was given. The workflow uses no secrets; it scans the whole history with gitleaks and the tree with the check,
on every push and weekly.

`bash scripts/install.sh` points this clone's git hooks at `scripts/githooks` (commits and pushes run the
check), unless the machine sets a global or system hooks path, which it leaves in force: there, run
`bash scripts/check-public.sh` before committing. On a Dot it also disables pushing and refuses to set up next
to a GitHub credential. `bash scripts/test-leak-guard.sh` plants leaks in throwaway repos and checks that every
guard catches them. Rules for agents: [AGENTS.md](AGENTS.md).
