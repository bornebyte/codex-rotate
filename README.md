# codex-rotate

A small, dependency-free Go CLI for rotating between multiple [OpenAI Codex CLI](https://github.com/openai/codex) accounts on the same machine.

If you juggle several Codex accounts and switch to a fresh one whenever the current one hits its usage quota, `codex-rotate` keeps every account's `auth.json` safely on disk, lets you label them, and swaps the active one in and out with a single command — **nothing is ever deleted**, only moved.

```
$ codex-rotate list
  NAME    NICKNAME     ADDED       LAST USED   DESCRIPTION
* work    Work acct    2026-07-01  2 days ago  Company-issued seat
  alt-1   Personal 1   2026-07-14  9 days ago  Free tier, resets monthly
  alt-2   Personal 2   2026-08-02  never       Backup account

* = active (work)
```

## Why

The Codex CLI reads exactly one file: `~/.codex/auth.json`. If you have more
than one account, the naive workflow is:

1. `codex login` into account A, use it until it's rate-limited.
2. `codex login` into account B — this **overwrites** `auth.json`, so A's
   session is gone unless you'd copied it somewhere first.
3. Repeat, and hope you remember which backup file was which account.

`codex-rotate` formalizes step 2: it parks the outgoing account's
`auth.json` into `~/.codex/profiles/<name>.json` and restores the incoming
one in its place — an atomic rename in each direction, never a copy, never
a delete.

## Install

Requires Go 1.22+.

```bash
git clone https://github.com/bornebyte/codex-rotate.git
cd codex-rotate
go build -o codex-rotate .
sudo mv codex-rotate /usr/local/bin/        # or anywhere on your $PATH
```

Or, without cloning:

```bash
go install github.com/bornebyte/codex-rotate@latest
```

## Quick start

```bash
# You're already logged in via `codex login`. Register that session:
codex-rotate capture work --nickname "Work acct" --description "Company-issued seat"

# Hit a quota limit. Park it, log into another account, capture that too:
codex-rotate park
codex login
codex-rotate capture alt-1 -n "Personal 1"

# See everything you have:
codex-rotate list

# Check remaining quota and reset times for every account, no switching required:
codex-rotate stats

# Rotate back to "work" later (interactive picker if you omit the name):
codex-rotate switch work
```

## Commands

| Command | What it does |
|---|---|
| `list` | Show every tracked profile, which one is active, and when each was last used. Also flags "drift" (see below). |
| `capture <name> [-n nick] [-d desc]` | Register the file currently at `~/.codex/auth.json` as a new profile and mark it active. Use right after `codex login`. |
| `switch [name]` | Park the active profile and restore `<name>` in its place. No name → interactive numbered picker. |
| `park [name]` | Move the active profile's `auth.json` into storage and leave nothing active. Do this *before* running `codex login` again, so the outgoing session isn't clobbered. |
| `rename <old> <new>` | Rename a profile, including its on-disk file if parked. |
| `nickname <name> <text>` | Set or replace a display nickname. |
| `describe <name> <text>` | Set or replace a free-text description. |
| `current` | Show details of whichever profile is active right now. |
| `repair` | If `auth.json` was changed outside this tool (e.g. a manual `codex login` refreshed the same account's token) and `list`/`current` report drift, run this to accept the new content as that profile's current state. |
| `stats [name]` | Show live quota — 5-hour and weekly used%, reset times, plan, and email — for every tracked profile, or just one. No switching involved; see below for how. |
| `completion <bash\|zsh\|fish>` | Print a shell completion script. See below for setup. |

Aliases: `ls`→`list`, `add`/`import`→`capture`, `rotate`/`use`→`switch`, `mv`→`rename`, `nick`→`nickname`, `desc`→`describe`, `whoami`→`current`, `usage`/`quota`→`stats`.

### `stats`: quota without switching

```
$ codex-rotate stats
  NAME    EMAIL               PLAN  PRIMARY USED  PRIMARY LEFT  PRIMARY RESETS                     SECONDARY USED  SECONDARY LEFT  SECONDARY RESETS  STATUS
* work    user1@example.com  pro   69%           31%           2h14m (Sep 14 18:00) [5h window]    55%             45%             6h02m (Sep 21 00:34) [7d window]  ok
  alt-1   user2@example.com  go    ⚠ 100%        0%            345h55m (Sep 29 10:00) [30d window]  -               -               -                 ok
  alt-2   -                  -     -             -             -                                    -               -               -                 error: auth token expired — switch into it, run `codex login`, then `capture` it again
```

`PRIMARY`/`SECONDARY` are deliberately generic labels, not "5h"/"weekly" —
those durations only hold for some plans. A free or Go plan account often
reports a single long-lived `PRIMARY` window (weeks, not hours) and no
`SECONDARY` window at all, while Pro/Team plans report a genuine 5-hour +
7-day pair. Rather than assert a duration that's sometimes wrong, the real
window length is printed in brackets next to each reset time, straight from
what the account itself reports.

The `LEFT` columns are colored by how much runway you actually have —
green when you're fine, yellow once it's worth planning around, red once
you're about to get locked out (thresholds: ≤30% left = yellow, ≤10% left =
red). Color is skipped automatically when stdout isn't a terminal (piped to
a file, etc.) or when `NO_COLOR` is set; the plain percentage is always
printed either way, so no information is lost — just the color.

A `STATUS` error means exactly what it says about *that profile's stored
credentials* — most commonly an expired or invalid token — not a bug in
`stats` itself. Fix it the same way you'd fix drift: `codex-rotate switch
<name>` to bring it live, `codex login` to refresh it, then `codex-rotate
capture <name>` again to re-record it.

Previously the only way to see this was to `switch` into every account and
run `codex`'s own `/status`. `stats` instead spawns a short-lived
`codex app-server` process per profile — the same JSON-RPC-over-stdio
interface the Codex VS Code extension and Codex Desktop use internally
(see [the app-server docs](https://developers.openai.com/codex/app-server))
— and points it at a **copy** of that profile's `auth.json` sitting in a
scratch directory. This is why it's safe to run against parked profiles
without switching: the real `~/.codex/auth.json` and the parked
`profiles/<name>.json` files are only ever read, never written, moved, or
even opened for writing.

Profiles are queried concurrently (capped at 4 at a time) with a
25-second timeout each, so one stuck or unauthenticated profile can't hang
the rest — it just shows up with `error: ...` in the STATUS column while
the others report normally.

This depends on the `codex` binary being on your `PATH` and speaking the
`account/rateLimits/read` app-server method (Codex ≥ 0.130 or so); if
OpenAI changes that protocol's field names again, `stats` degrades to
blank `EMAIL`/`PLAN` columns rather than crashing outright — see the
comments in `appserver.go` for exactly which fields it expects.

## Shell completion

`codex-rotate completion <shell>` prints a completion script for bash, zsh,
or fish. Every script dynamically completes profile names for `switch`,
`park`, `rename`, `nickname`, `describe`, and `stats` by shelling out to a
hidden `codex-rotate __profiles` subcommand (one name per line, no
formatting) — so `switch <TAB>` completes to your actual profiles, not just
the word "switch".

**bash** — either add to `~/.bashrc`:

```bash
eval "$(codex-rotate completion bash)"
```

or install it system-wide (picked up automatically by bash-completion):

```bash
codex-rotate completion bash | sudo tee /etc/bash_completion.d/codex-rotate
```

**zsh** — save it as a file named `_codex-rotate` somewhere in your
`$fpath`, then start a new shell:

```bash
codex-rotate completion zsh > "${fpath[1]}/_codex-rotate"
```

If completions don't show up, your `~/.zshrc` may not be running
`compinit` — add `autoload -U compinit && compinit` and restart the shell.

**fish** — fish auto-loads anything in its completions directory, so this
is a one-time step:

```fish
codex-rotate completion fish > ~/.config/fish/completions/codex-rotate.fish
```

After installing, `codex-rotate swi<TAB>` completes to `switch`, and
`codex-rotate switch <TAB>` completes to your tracked profile names.

## How it works

- **One live copy.** Only the *active* profile's bytes ever sit at
  `~/.codex/auth.json`; every other profile's file lives at
  `~/.codex/profiles/<name>.json`. Rotating is exactly two `os.Rename`
  calls — atomic on the same filesystem, so a crash mid-rotation can't
  leave you with zero or two copies of a session.
- **Metadata is separate from credentials.** Nicknames, descriptions, and
  timestamps live in `~/.codex/profiles/store.json`, written with a
  write-temp-then-rename pattern so it's never left half-written.
- **Drift detection.** Every tracked profile records a SHA-256 of its
  `auth.json` content. `list` and `current` recompute the hash of the live
  file and warn you if it no longer matches — the signal that someone ran
  `codex login` by hand instead of going through `park`/`capture`, which is
  the one way a session's old bytes could be lost (Codex overwrites
  `auth.json` in place; the tool can't recover bytes it never got a chance
  to park).
- **No dependencies.** Standard library only — `crypto/sha256`,
  `encoding/json`, `os`, `text/tabwriter`. Nothing to vendor, no supply
  chain to audit beyond the Go toolchain itself.

## File layout

```
~/.codex/auth.json                 the credential file Codex CLI reads
~/.codex/profiles/store.json       metadata: names, nicknames, descriptions, timestamps, hashes
~/.codex/profiles/<name>.json      parked (inactive) auth.json files
```

`auth.json` contains live OAuth/API credentials — treat
`~/.codex/profiles/` with the same care you'd give `~/.ssh`. This repo's
`.gitignore` already excludes any local `.codex/` directory you might place
inside it for testing.

## Safety notes

- `codex-rotate` never calls `os.Remove` on a credential file. The only
  destructive-looking operation is `os.Rename`, and only ever between
  `auth.json` and `profiles/<name>.json`.
- If you `codex login` into a new account **without** running `park`
  first, the outgoing account's token is overwritten by the OS-level write
  Codex itself performs — this tool has no way to intercept that. `list`
  will flag the resulting drift after the fact, but the fix is procedural:
  always `park` before a fresh login.
- `store.json` and the parked profile files contain secrets. Back them up
  the way you'd back up any credential store, and don't commit them.

## License

MIT — see [LICENSE](LICENSE).