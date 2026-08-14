package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ProfileMeta is everything we track about one Codex account/session,
// independent of where its auth.json bytes currently live on disk.
type ProfileMeta struct {
	Name        string     `json:"name"`
	Nickname    string     `json:"nickname,omitempty"`
	Description string     `json:"description,omitempty"`
	AddedAt     time.Time  `json:"added_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	// SHA256 is the checksum of the auth.json content we last confirmed
	// for this profile. It's how `list`/`current` detect that someone ran
	// `codex login` by hand and silently overwrote the active session.
	SHA256 string `json:"sha256,omitempty"`
}

// Store is the full state persisted to profiles/store.json.
//
// Design note: only ONE profile's credential bytes ever live at
// ~/.codex/auth.json — the "active" one, referenced by Active. Every other
// tracked profile's auth.json is parked at profiles/<name>.json. Rotating
// is therefore always exactly two os.Rename calls (park + restore), never a
// copy, so nothing is ever duplicated or silently dropped.
type Store struct {
	Active   string                  `json:"active"`
	Profiles map[string]*ProfileMeta `json:"profiles"`
}

// Paths centralizes every filesystem location the tool touches.
type Paths struct {
	CodexDir    string
	AuthPath    string
	ProfilesDir string
	StorePath   string
}

func resolvePaths() (*Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	codexDir := filepath.Join(home, ".codex")
	profilesDir := filepath.Join(codexDir, "profiles")
	return &Paths{
		CodexDir:    codexDir,
		AuthPath:    filepath.Join(codexDir, "auth.json"),
		ProfilesDir: profilesDir,
		StorePath:   filepath.Join(profilesDir, "store.json"),
	}, nil
}

func (p *Paths) profileFile(name string) string {
	return filepath.Join(p.ProfilesDir, name+".json")
}

func loadStore(p *Paths) (*Store, error) {
	if err := os.MkdirAll(p.ProfilesDir, 0o700); err != nil {
		return nil, fmt.Errorf("create profiles directory: %w", err)
	}
	data, err := os.ReadFile(p.StorePath)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{Profiles: map[string]*ProfileMeta{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.StorePath, err)
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s (corrupted?): %w", p.StorePath, err)
	}
	if s.Profiles == nil {
		s.Profiles = map[string]*ProfileMeta{}
	}
	return &s, nil
}

// saveStore writes atomically: write-tmp-then-rename means a crash or a
// killed process can never leave store.json half-written or truncated.
func saveStore(p *Paths, s *Store) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal store: %w", err)
	}
	tmp := p.StorePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp store: %w", err)
	}
	if err := os.Rename(tmp, p.StorePath); err != nil {
		return fmt.Errorf("commit store: %w", err)
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sortedNames(s *Store) []string {
	names := make([]string, 0, len(s.Profiles))
	for n := range s.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	days := int(d.Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "1 day ago"
	default:
		return fmt.Sprintf("%d days ago", days)
	}
}

var nameRe = validNameChecker()

func validateName(name string) error {
	if name == "" {
		return errors.New("profile name cannot be empty")
	}
	if name == "store" {
		return errors.New(`"store" is reserved`)
	}
	if !nameRe(name) {
		return errors.New("profile name may only contain letters, digits, dashes, and underscores")
	}
	return nil
}

// validNameChecker avoids pulling in "regexp" for a single tiny check.
func validNameChecker() func(string) bool {
	return func(s string) bool {
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= 'A' && r <= 'Z':
			case r >= '0' && r <= '9':
			case r == '-' || r == '_':
			default:
				return false
			}
		}
		return true
	}
}
