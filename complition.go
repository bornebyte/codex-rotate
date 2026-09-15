package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// cmdCompletion prints a shell completion script for bash, zsh, or fish.
// It's handled before paths/store are loaded (see main.go) so that sourcing
// it on every new shell — e.g. `eval "$(codex-rotate completion bash)"` in
// ~/.bashrc — never touches ~/.codex as a side effect.
//
// Every script below shells back out to `codex-rotate __profiles`, a hidden
// subcommand (deliberately left off the `help` output) that just prints
// tracked profile names, one per line, with no header or formatting. That's
// what lets `switch <TAB>`, `park <TAB>`, etc. complete to your *actual*
// profile names instead of only completing the subcommand itself.
func cmdCompletion(args []string) error {
	if len(args) == 2 && args[0] == "bash" && args[1] == "--install" {
		return installBashCompletion()
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: codex-rotate completion <bash|zsh|fish> [--install]")
	}
	switch args[0] {
	case "bash":
		fmt.Print(bashCompletionScript)
	case "zsh":
		fmt.Print(zshCompletionScript)
	case "fish":
		fmt.Print(fishCompletionScript)
	default:
		return fmt.Errorf("unsupported shell %q — choose bash, zsh, or fish", args[0])
	}
	return nil
}

// installBashCompletion enables completion for future Bash shells without
// making `completion bash` unsafe to use in command substitution. The latter
// must print only shell code because users commonly run:
//
//	eval "$(codex-rotate completion bash)"
func installBashCompletion() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	bashrc := filepath.Join(home, ".bashrc")
	line := []byte(`eval "$(codex-rotate completion bash)"`)

	data, err := os.ReadFile(bashrc)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", bashrc, err)
	}
	if bytes.Contains(data, line) {
		fmt.Printf("Bash completion is already enabled in %s.\n", bashrc)
		return nil
	}

	f, err := os.OpenFile(bashrc, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", bashrc, err)
	}
	defer f.Close()
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return fmt.Errorf("update %s: %w", bashrc, err)
		}
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("update %s: %w", bashrc, err)
	}
	fmt.Printf("Enabled Bash completion in %s. Open a new shell to use it.\n", bashrc)
	return nil
}

// commandsForCompletion must track the `switch cmd` cases in main.go —
// every top-level command name and alias, plus `completion` and `help`.
// It deliberately excludes `__profiles`, which exists only for these
// scripts to call and was never meant to be typed or suggested.
const commandWordsForCompletion = "list ls capture add import switch swap rotate use park rename mv " +
	"delete del remove rm nickname nick describe desc current whoami repair stats usage quota completion help"

// profileArgCommandWords are the subcommands whose very next argument is an
// existing profile name — that's where `__profiles` output gets suggested.
const profileArgCommandWords = "switch swap rotate use park rename mv delete del remove rm nickname nick describe desc stats usage quota"

const bashCompletionScript = `# bash completion for codex-rotate
# Install (pick one):
#   codex-rotate completion bash --install
#   echo 'eval "$(codex-rotate completion bash)"' >> ~/.bashrc
#   codex-rotate completion bash | sudo tee /etc/bash_completion.d/codex-rotate

_codex_rotate_completions() {
    local cur prev
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "` + commandWordsForCompletion + `" -- "${cur}") )
        return 0
    fi

    case "${prev}" in
        switch|swap|rotate|use|park|rename|mv|delete|del|remove|rm|nickname|nick|describe|desc|stats|usage|quota)
            COMPREPLY=( $(compgen -W "$(codex-rotate __profiles 2>/dev/null)" -- "${cur}") )
            return 0
            ;;
        capture|add|import)
            COMPREPLY=( $(compgen -W "-n --nickname -d --description" -- "${cur}") )
            return 0
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            return 0
            ;;
    esac

    COMPREPLY=()
}
complete -F _codex_rotate_completions codex-rotate
`

const zshCompletionScript = `#compdef codex-rotate
# zsh completion for codex-rotate
# Install:
#   codex-rotate completion zsh > "${fpath[1]}/_codex-rotate"
#   (then start a new shell, or run: autoload -U compinit && compinit)

_codex_rotate() {
    local -a profile_arg_commands
    profile_arg_commands=(` + profileArgCommandWords + `)

    if (( CURRENT == 2 )); then
        local -a commands
        commands=(` + commandWordsForCompletion + `)
        _describe 'command' commands
        return
    fi

    local cmd=${words[2]}

    if (( ${profile_arg_commands[(Ie)$cmd]} )) && (( CURRENT == 3 )); then
        local -a profiles
        profiles=(${(f)"$(codex-rotate __profiles 2>/dev/null)"})
        _describe 'profile' profiles
        return
    fi

    if [[ $cmd == completion ]] && (( CURRENT == 3 )); then
        _values 'shell' bash zsh fish
    fi
}

_codex_rotate "$@"
`

const fishCompletionScript = `# fish completion for codex-rotate
# Install:
#   codex-rotate completion fish > ~/.config/fish/completions/codex-rotate.fish

set -l __codex_rotate_commands ` + commandWordsForCompletion + `
set -l __codex_rotate_profile_arg_commands ` + profileArgCommandWords + `

complete -c codex-rotate -f

complete -c codex-rotate -n "not __fish_seen_subcommand_from $__codex_rotate_commands" -a "$__codex_rotate_commands"
complete -c codex-rotate -n "__fish_seen_subcommand_from $__codex_rotate_profile_arg_commands" -a "(codex-rotate __profiles 2>/dev/null)"
complete -c codex-rotate -n "__fish_seen_subcommand_from completion" -a "bash zsh fish"
`
