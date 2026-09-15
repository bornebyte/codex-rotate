// Command codex-rotate manages multiple OpenAI Codex CLI (`codex`) auth.json
// profiles on one machine, so you can rotate between accounts when one hits
// its usage quota without losing a session unless you explicitly delete it.
package main

import (
	"fmt"
	"os"
)

const usage = `codex-rotate — rotate Codex CLI auth profiles

Usage:
  codex-rotate list                            List all tracked profiles
  codex-rotate capture <name> [-n nick] [-d desc]
                                                 Register the current ~/.codex/auth.json
                                                 as a new profile and mark it active
  codex-rotate switch [name]                    Rotate to another profile (interactive
                                                 picker if no name given)
  codex-rotate swap [name]                      Swap in a profile even if the current
                                                 auth.json has no active marker
  codex-rotate park [name]                      Move the active profile's auth.json into
                                                 storage without activating anything else
                                                 (do this before a fresh 'codex login')
  codex-rotate rename <old> <new>               Rename a profile
  codex-rotate delete <name>                    Delete an inactive profile and its stored
                                                 credentials (park it first if active)
  codex-rotate delete-expired                   Find profiles with expired auth tokens,
                                                 show them, and confirm before deleting
  codex-rotate nickname <name> <nick...>        Set/replace a profile's nickname
  codex-rotate describe <name> [desc...]        Set/replace or clear a profile's description
  codex-rotate current                          Show details of the active profile
  codex-rotate repair                           Accept the live auth.json as the active
                                                 profile's current state after drift
  codex-rotate stats [name]                     Show live quota (5h/weekly used%, reset
                                                 times), plan, and email for every
                                                 profile — or just one — without
                                                 switching into any of them
  codex-rotate completion <bash|zsh|fish>       Print a shell completion script
  codex-rotate completion bash --install        Add Bash completion to ~/.bashrc
  codex-rotate help                             Show this message

Files touched:
  ~/.codex/auth.json               The credential file the Codex CLI reads
  ~/.codex/profiles/<name>.json    Parked (inactive) profiles
  ~/.codex/profiles/store.json     Metadata: nicknames, descriptions, timestamps

Profiles are only deleted when you explicitly run 'delete'. The active profile
must be parked first, so a live auth.json cannot be erased accidentally.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Print(usage)
		return
	}
	if cmd == "completion" {
		if err := cmdCompletion(args); err != nil {
			fatal(err)
		}
		return
	}

	paths, err := resolvePaths()
	if err != nil {
		fatal(err)
	}
	store, err := loadStore(paths)
	if err != nil {
		fatal(err)
	}

	switch cmd {
	case "list", "ls":
		err = cmdList(paths, store)
	case "capture", "add", "import":
		err = cmdCapture(paths, store, args)
	case "park":
		err = cmdPark(paths, store, args)
	case "switch", "rotate", "use":
		err = cmdSwitch(paths, store, args)
	case "swap":
		err = cmdSwap(paths, store, args)
	case "rename", "mv":
		err = cmdRename(paths, store, args)
	case "delete", "del", "remove", "rm":
		err = cmdDelete(paths, store, args)
	case "delete-expired", "purge-expired":
		err = cmdDeleteExpired(paths, store, args)
	case "nickname", "nick":
		err = cmdNickname(paths, store, args)
	case "describe", "desc":
		err = cmdDescribe(paths, store, args)
	case "current", "whoami":
		err = cmdCurrent(paths, store)
	case "repair":
		err = cmdRepair(paths, store)
	case "stats", "usage", "quota":
		err = cmdStats(paths, store, args)
	case "__profiles": // internal: used by the shell completion scripts only
		err = cmdProfileNames(store)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Print(usage)
		os.Exit(1)
	}

	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
