package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// ---------- list ----------

func cmdList(p *Paths, s *Store) error {
	checkDrift(p, s) // prints a warning if present; non-fatal

	if len(s.Profiles) == 0 {
		fmt.Println("No profiles tracked yet.")
		fmt.Println()
		fmt.Println("If you're already logged in (~/.codex/auth.json exists), run:")
		fmt.Println("  codex-rotate capture <name>")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "  NAME\tNICKNAME\tADDED\tLAST USED\tDESCRIPTION")
	for _, n := range sortedNames(s) {
		m := s.Profiles[n]
		marker := " "
		if n == s.Active {
			marker = "*"
		}
		last := "never"
		if m.LastUsedAt != nil {
			last = humanAgo(*m.LastUsedAt)
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\n",
			marker, n, m.Nickname, m.AddedAt.Format("2006-01-02"), last, m.Description)
	}
	w.Flush()

	if s.Active != "" {
		fmt.Printf("\n* = active (%s)\n", s.Active)
	} else {
		fmt.Println("\nNo profile is currently active. Run `codex-rotate switch <name>`.")
	}
	return nil
}

// checkDrift compares the live auth.json against the hash we last recorded
// for the active profile. A mismatch means something outside this tool
// (typically a manual `codex login`) overwrote it.
func checkDrift(p *Paths, s *Store) {
	if s.Active == "" || !fileExists(p.AuthPath) {
		return
	}
	meta, ok := s.Profiles[s.Active]
	if !ok || meta.SHA256 == "" {
		return
	}
	h, err := hashFile(p.AuthPath)
	if err != nil || h == meta.SHA256 {
		return
	}
	fmt.Println("⚠  auth.json no longer matches the tracked profile", strconv.Quote(s.Active)+".")
	fmt.Println("   Looks like `codex login` was run by hand and overwrote it in place.")
	fmt.Println("   - If this IS still", s.Active, "(e.g. a token refresh), run: codex-rotate repair")
	fmt.Println("   - If this is a NEW account, run:                        codex-rotate capture <new-name>")
	fmt.Println()
}

// ---------- capture ----------

func cmdCapture(p *Paths, s *Store, args []string) error {
	name, nickname, description, err := parseNameAndFlags(args)
	if err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return err
	}
	if !fileExists(p.AuthPath) {
		return fmt.Errorf("no auth.json at %s — run `codex login` first", p.AuthPath)
	}
	if s.Active != "" && s.Active != name {
		return fmt.Errorf(
			"profile %q is currently active — capturing now would treat its live session as %q.\n"+
				"Run `codex-rotate park` first (moves %q safely into storage), then log in and capture again",
			s.Active, name, s.Active)
	}
	if _, exists := s.Profiles[name]; exists && s.Active != name {
		return fmt.Errorf("profile %q already exists — pick another name or use `codex-rotate switch %s`", name, name)
	}

	hash, err := hashFile(p.AuthPath)
	if err != nil {
		return fmt.Errorf("hash auth.json: %w", err)
	}

	now := time.Now()
	meta, exists := s.Profiles[name]
	if !exists {
		meta = &ProfileMeta{Name: name, AddedAt: now}
		s.Profiles[name] = meta
	}
	if nickname != "" {
		meta.Nickname = nickname
	}
	if description != "" {
		meta.Description = description
	}
	meta.SHA256 = hash
	meta.LastUsedAt = &now
	s.Active = name

	if err := saveStore(p, s); err != nil {
		return err
	}
	fmt.Printf("Captured %q as the active profile.\n", name)
	return nil
}

// ---------- park ----------

func cmdPark(p *Paths, s *Store, args []string) error {
	name := s.Active
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" {
		if fileExists(p.AuthPath) {
			return fmt.Errorf("auth.json exists but isn't tracked yet.\n" +
				"Run `codex-rotate capture <name>` to register it, or `codex-rotate park <name>` to register-and-park in one step")
		}
		return fmt.Errorf("no active profile to park")
	}
	if err := validateName(name); err != nil {
		return err
	}
	if err := doPark(p, s, name); err != nil {
		return err
	}
	if err := saveStore(p, s); err != nil {
		return err
	}
	fmt.Printf("Parked %q → %s\n", name, p.profileFile(name))
	fmt.Println("Safe to `codex login` into a different account now.")
	return nil
}

// doPark moves the live auth.json into profiles/<name>.json and clears
// Active. It mutates s in memory but does NOT save — callers that chain
// multiple operations (e.g. switch = park + restore) save once at the end.
func doPark(p *Paths, s *Store, name string) error {
	if !fileExists(p.AuthPath) {
		return fmt.Errorf("no auth.json present at %s — nothing to park", p.AuthPath)
	}
	dest := p.profileFile(name)
	if fileExists(dest) {
		return fmt.Errorf("internal conflict: %s already exists — refusing to overwrite it", dest)
	}
	hash, err := hashFile(p.AuthPath)
	if err != nil {
		return fmt.Errorf("hash auth.json: %w", err)
	}
	now := time.Now()
	meta, exists := s.Profiles[name]
	if !exists {
		meta = &ProfileMeta{Name: name, AddedAt: now}
		s.Profiles[name] = meta
	}
	meta.SHA256 = hash
	meta.LastUsedAt = &now

	if err := os.Rename(p.AuthPath, dest); err != nil {
		return fmt.Errorf("move auth.json to storage: %w", err)
	}
	if s.Active == name {
		s.Active = ""
	}
	return nil
}

// ---------- switch ----------

func cmdSwitch(p *Paths, s *Store, args []string) error {
	if len(args) == 0 {
		return interactiveSwitch(p, s)
	}
	return switchTo(p, s, args[0])
}

func switchTo(p *Paths, s *Store, name string) error {
	meta, ok := s.Profiles[name]
	if !ok {
		return fmt.Errorf("no such profile %q — run `codex-rotate list`", name)
	}
	if s.Active == name {
		fmt.Printf("%q is already active.\n", name)
		return nil
	}
	if s.Active != "" {
		if err := doPark(p, s, s.Active); err != nil {
			return fmt.Errorf("could not park current profile %q before switching: %w", s.Active, err)
		}
	}
	src := p.profileFile(name)
	if !fileExists(src) {
		return fmt.Errorf("profile %q has no stored auth file at %s — it may have been moved or edited outside codex-rotate", name, src)
	}
	if err := os.Rename(src, p.AuthPath); err != nil {
		return fmt.Errorf("restore %q into place: %w", name, err)
	}
	now := time.Now()
	meta.LastUsedAt = &now
	if h, err := hashFile(p.AuthPath); err == nil {
		meta.SHA256 = h
	}
	s.Active = name

	if err := saveStore(p, s); err != nil {
		return err
	}
	label := name
	if meta.Nickname != "" {
		label = fmt.Sprintf("%s (%s)", name, meta.Nickname)
	}
	fmt.Printf("Switched to %s.\n", label)
	return nil
}

func interactiveSwitch(p *Paths, s *Store) error {
	names := sortedNames(s)
	if len(names) == 0 {
		return fmt.Errorf("no profiles registered yet — run `codex-rotate capture <name>` first")
	}
	fmt.Println("Available profiles:")
	for i, n := range names {
		m := s.Profiles[n]
		marker := " "
		if n == s.Active {
			marker = "*"
		}
		label := n
		if m.Nickname != "" {
			label = fmt.Sprintf("%s (%s)", n, m.Nickname)
		}
		last := "never used"
		if m.LastUsedAt != nil {
			last = humanAgo(*m.LastUsedAt)
		}
		fmt.Printf(" %s [%d] %-30s last used: %s\n", marker, i+1, label, last)
	}
	fmt.Print("\nSwitch to # (blank to cancel): ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		fmt.Println("Cancelled.")
		return nil
	}
	idx, err := strconv.Atoi(line)
	if err != nil || idx < 1 || idx > len(names) {
		return fmt.Errorf("invalid selection %q", line)
	}
	return switchTo(p, s, names[idx-1])
}

// ---------- rename / nickname / describe ----------

func cmdRename(p *Paths, s *Store, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: codex-rotate rename <old-name> <new-name>")
	}
	oldName, newName := args[0], args[1]
	if err := validateName(newName); err != nil {
		return err
	}
	meta, ok := s.Profiles[oldName]
	if !ok {
		return fmt.Errorf("no such profile %q", oldName)
	}
	if _, exists := s.Profiles[newName]; exists {
		return fmt.Errorf("a profile named %q already exists", newName)
	}
	if oldName != s.Active {
		src := p.profileFile(oldName)
		if fileExists(src) {
			if err := os.Rename(src, p.profileFile(newName)); err != nil {
				return fmt.Errorf("rename stored file: %w", err)
			}
		}
	} else {
		s.Active = newName
	}
	meta.Name = newName
	s.Profiles[newName] = meta
	delete(s.Profiles, oldName)

	if err := saveStore(p, s); err != nil {
		return err
	}
	fmt.Printf("Renamed %q → %q.\n", oldName, newName)
	return nil
}

func cmdNickname(p *Paths, s *Store, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: codex-rotate nickname <name> <nickname...>")
	}
	meta, ok := s.Profiles[args[0]]
	if !ok {
		return fmt.Errorf("no such profile %q", args[0])
	}
	meta.Nickname = strings.Join(args[1:], " ")
	if err := saveStore(p, s); err != nil {
		return err
	}
	fmt.Printf("Nickname for %q set to %q.\n", args[0], meta.Nickname)
	return nil
}

func cmdDescribe(p *Paths, s *Store, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: codex-rotate describe <name> <description...>")
	}
	meta, ok := s.Profiles[args[0]]
	if !ok {
		return fmt.Errorf("no such profile %q", args[0])
	}
	meta.Description = strings.Join(args[1:], " ")
	if err := saveStore(p, s); err != nil {
		return err
	}
	fmt.Printf("Description for %q updated.\n", args[0])
	return nil
}

// ---------- current / repair ----------

func cmdCurrent(p *Paths, s *Store) error {
	checkDrift(p, s)
	if s.Active == "" {
		fmt.Println("No profile is currently active.")
		return nil
	}
	m := s.Profiles[s.Active]
	fmt.Printf("Active profile: %s\n", s.Active)
	if m.Nickname != "" {
		fmt.Printf("Nickname:       %s\n", m.Nickname)
	}
	if m.Description != "" {
		fmt.Printf("Description:    %s\n", m.Description)
	}
	fmt.Printf("Added:          %s\n", m.AddedAt.Format("2006-01-02 15:04"))
	if m.LastUsedAt != nil {
		fmt.Printf("Last rotated:   %s\n", humanAgo(*m.LastUsedAt))
	}
	fmt.Printf("File:           %s\n", p.AuthPath)
	return nil
}

func cmdRepair(p *Paths, s *Store) error {
	if s.Active == "" {
		return fmt.Errorf("no active profile to repair")
	}
	if !fileExists(p.AuthPath) {
		return fmt.Errorf("no auth.json present at %s", p.AuthPath)
	}
	h, err := hashFile(p.AuthPath)
	if err != nil {
		return err
	}
	meta := s.Profiles[s.Active]
	meta.SHA256 = h
	now := time.Now()
	meta.LastUsedAt = &now
	if err := saveStore(p, s); err != nil {
		return err
	}
	fmt.Printf("Recorded current auth.json contents as %q's latest session.\n", s.Active)
	return nil
}

// ---------- flag parsing helper ----------

// parseNameAndFlags handles: capture <name> [-n|--nickname X] [-d|--description Y]
func parseNameAndFlags(args []string) (name, nickname, description string, err error) {
	if len(args) == 0 {
		return "", "", "", fmt.Errorf("usage: codex-rotate capture <name> [-n nickname] [-d description]")
	}
	name = args[0]
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "-n", "--nickname":
			if i+1 >= len(rest) {
				return "", "", "", fmt.Errorf("%s requires a value", rest[i])
			}
			i++
			nickname = rest[i]
		case "-d", "--description":
			if i+1 >= len(rest) {
				return "", "", "", fmt.Errorf("%s requires a value", rest[i])
			}
			i++
			description = rest[i]
		default:
			return "", "", "", fmt.Errorf("unknown flag %q", rest[i])
		}
	}
	return name, nickname, description, nil
}
