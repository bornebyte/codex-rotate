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
git clone https://github.com/<you>/codex-rotate.git
cd codex-rotate
go build -o codex-rotate .
sudo mv codex-rotate /usr/local/bin/        # or anywhere on your $PATH
```

Or, without cloning:

```bash
go install github.com/<you>/codex-rotate@latest
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

Aliases: `ls`→`list`, `add`/`import`→`capture`, `rotate`/`use`→`switch`, `mv`→`rename`, `nick`→`nickname`, `desc`→`describe`, `whoami`→`current`.

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
