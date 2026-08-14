// Command codex-rotate manages multiple OpenAI Codex CLI (`codex`) auth.json
// profiles on one machine, so you can rotate between accounts when one hits
// its usage quota without ever deleting a session's credentials.
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
  codex-rotate park [name]                      Move the active profile's auth.json into
                                                 storage without activating anything else
                                                 (do this before a fresh 'codex login')
  codex-rotate rename <old> <new>               Rename a profile
  codex-rotate nickname <name> <nick...>        Set/replace a profile's nickname
  codex-rotate describe <name> <desc...>        Set/replace a profile's description
  codex-rotate current                          Show details of the active profile
  codex-rotate repair                           Accept the live auth.json as the active
                                                 profile's current state after drift
  codex-rotate help                             Show this message

Files touched:
  ~/.codex/auth.json               The credential file the Codex CLI reads
  ~/.codex/profiles/<name>.json    Parked (inactive) profiles
  ~/.codex/profiles/store.json     Metadata: nicknames, descriptions, timestamps

Nothing is ever deleted — profiles are only moved between ~/.codex/auth.json
and ~/.codex/profiles/, so credentials are never lost, only relocated.
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
	case "rename", "mv":
		err = cmdRename(paths, store, args)
	case "nickname", "nick":
		err = cmdNickname(paths, store, args)
	case "describe", "desc":
		err = cmdDescribe(paths, store, args)
	case "current", "whoami":
		err = cmdCurrent(paths, store)
	case "repair":
		err = cmdRepair(paths, store)
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
