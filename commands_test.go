package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testPaths(t *testing.T) *Paths {
	t.Helper()
	root := t.TempDir()
	profiles := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	return &Paths{
		CodexDir:    root,
		AuthPath:    filepath.Join(root, "auth.json"),
		ProfilesDir: profiles,
		StorePath:   filepath.Join(profiles, "store.json"),
	}
}

func testStore(name string) *Store {
	return &Store{
		Profiles: map[string]*ProfileMeta{
			name: {Name: name, AddedAt: time.Now()},
		},
	}
}

func TestDeleteParkedProfile(t *testing.T) {
	p := testPaths(t)
	s := testStore("old")
	if err := os.WriteFile(p.profileFile("old"), []byte("old credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdDelete(p, s, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	if fileExists(p.profileFile("old")) {
		t.Fatal("delete left the parked credential file behind")
	}
	if _, ok := s.Profiles["old"]; ok {
		t.Fatal("delete left metadata behind")
	}

	loaded, err := loadStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Profiles["old"]; ok {
		t.Fatal("delete left persisted metadata behind")
	}
}

func TestDeleteActiveProfileRequiresPark(t *testing.T) {
	p := testPaths(t)
	s := testStore("current")
	s.Active = "current"
	if err := os.WriteFile(p.AuthPath, []byte("live credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdDelete(p, s, []string{"current"}); err == nil {
		t.Fatal("delete unexpectedly removed the active profile")
	}
	if !fileExists(p.AuthPath) {
		t.Fatal("delete removed the active auth.json")
	}
	if _, ok := s.Profiles["current"]; !ok {
		t.Fatal("delete removed active metadata after refusing the operation")
	}
}

func TestRenameParkedProfileMovesCredentials(t *testing.T) {
	p := testPaths(t)
	s := testStore("old")
	if err := os.WriteFile(p.profileFile("old"), []byte("old credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdRename(p, s, []string{"old", "new"}); err != nil {
		t.Fatal(err)
	}
	if fileExists(p.profileFile("old")) || !fileExists(p.profileFile("new")) {
		t.Fatal("rename did not move the parked credential file")
	}
	if _, ok := s.Profiles["old"]; ok {
		t.Fatal("rename left old metadata behind")
	}
	if _, ok := s.Profiles["new"]; !ok {
		t.Fatal("rename did not create new metadata")
	}
}

func TestDescribeCanClearDescription(t *testing.T) {
	p := testPaths(t)
	s := testStore("work")
	s.Profiles["work"].Description = "stale description"

	if err := cmdDescribe(p, s, []string{"work"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Profiles["work"].Description; got != "" {
		t.Fatalf("description = %q, want empty", got)
	}
}

func TestCaptureRefreshesExistingActiveProfile(t *testing.T) {
	p := testPaths(t)
	s := testStore("work")
	s.Active = "work"
	oldAddedAt := s.Profiles["work"].AddedAt
	if err := os.WriteFile(p.AuthPath, []byte("refreshed credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdCapture(p, s, []string{"work"}); err != nil {
		t.Fatal(err)
	}
	if s.Profiles["work"].AddedAt != oldAddedAt {
		t.Fatal("refresh changed the profile's original added timestamp")
	}
	wantHash, err := hashFile(p.AuthPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Profiles["work"].SHA256; got != wantHash {
		t.Fatalf("sha256 = %q, want %q", got, wantHash)
	}
}

func TestSwitchMissingTargetDoesNotParkCurrent(t *testing.T) {
	p := testPaths(t)
	s := testStore("current")
	s.Active = "current"
	s.Profiles["other"] = &ProfileMeta{Name: "other", AddedAt: time.Now()}
	if err := os.WriteFile(p.AuthPath, []byte("live credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := switchTo(p, s, "other"); err == nil {
		t.Fatal("switch unexpectedly succeeded without a target file")
	}
	if !fileExists(p.AuthPath) {
		t.Fatal("switch parked the current auth.json before validating its target")
	}
	if fileExists(p.profileFile("current")) {
		t.Fatal("switch left the current profile parked after refusing the target")
	}
	if s.Active != "current" {
		t.Fatalf("active profile = %q, want current", s.Active)
	}
}

func TestSwitchSwapsCurrentAndStoredAuth(t *testing.T) {
	p := testPaths(t)
	s := testStore("current")
	s.Active = "current"
	s.Profiles["other"] = &ProfileMeta{Name: "other", AddedAt: time.Now()}
	if err := os.WriteFile(p.AuthPath, []byte("current credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.profileFile("other"), []byte("other credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := switchTo(p, s, "other"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(p.AuthPath); err != nil || string(got) != "other credentials" {
		t.Fatalf("auth.json = %q, err = %v; want other credentials", got, err)
	}
	if got, err := os.ReadFile(p.profileFile("current")); err != nil || string(got) != "current credentials" {
		t.Fatalf("parked current profile = %q, err = %v; want current credentials", got, err)
	}
	if s.Active != "other" {
		t.Fatalf("active profile = %q, want other", s.Active)
	}
}

func TestSwapWorksBeforeExplicitPark(t *testing.T) {
	p := testPaths(t)
	s := testStore("current")
	s.Active = "current"
	s.Profiles["other"] = &ProfileMeta{Name: "other", AddedAt: time.Now()}
	if err := os.WriteFile(p.AuthPath, []byte("current credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.profileFile("other"), []byte("other credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdSwap(p, s, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(p.AuthPath); err != nil || string(got) != "other credentials" {
		t.Fatalf("auth.json = %q, err = %v; want other credentials", got, err)
	}
	if got, err := os.ReadFile(p.profileFile("current")); err != nil || string(got) != "current credentials" {
		t.Fatalf("parked current profile = %q, err = %v; want current credentials", got, err)
	}
}

func TestSwapWorksAfterCurrentWasParked(t *testing.T) {
	p := testPaths(t)
	s := testStore("current")
	s.Profiles["other"] = &ProfileMeta{Name: "other", AddedAt: time.Now()}
	if err := os.WriteFile(p.profileFile("current"), []byte("current credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.profileFile("other"), []byte("other credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdSwap(p, s, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(p.AuthPath); err != nil || string(got) != "other credentials" {
		t.Fatalf("auth.json = %q, err = %v; want other credentials", got, err)
	}
	if s.Active != "other" {
		t.Fatalf("active profile = %q, want other", s.Active)
	}
}

func TestSwapFindsLiveProfileWithoutActiveMarker(t *testing.T) {
	p := testPaths(t)
	s := testStore("current")
	s.Profiles["other"] = &ProfileMeta{Name: "other", AddedAt: time.Now()}
	if err := os.WriteFile(p.AuthPath, []byte("current credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := hashFile(p.AuthPath)
	if err != nil {
		t.Fatal(err)
	}
	s.Profiles["current"].SHA256 = h
	if err := os.WriteFile(p.profileFile("other"), []byte("other credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdSwap(p, s, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(p.profileFile("current")); err != nil || string(got) != "current credentials" {
		t.Fatalf("parked current profile = %q, err = %v; want current credentials", got, err)
	}
	if s.Active != "other" {
		t.Fatalf("active profile = %q, want other", s.Active)
	}
}

func TestSwapPreservesUnknownLiveAuthWithoutActiveMarker(t *testing.T) {
	p := testPaths(t)
	s := testStore("current")
	s.Profiles["other"] = &ProfileMeta{Name: "other", AddedAt: time.Now()}
	if err := os.WriteFile(p.AuthPath, []byte("unknown live credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.profileFile("other"), []byte("other credentials"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdSwap(p, s, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(p.AuthPath); err != nil || string(got) != "other credentials" {
		t.Fatalf("auth.json = %q, err = %v; want other credentials", got, err)
	}
	if s.Active != "other" {
		t.Fatalf("active profile = %q, want other", s.Active)
	}

	entries, err := os.ReadDir(p.ProfilesDir)
	if err != nil {
		t.Fatal(err)
	}
	foundBackup := false
	for _, entry := range entries {
		if len(entry.Name()) < len(".codex-rotate-untracked-auth-") || entry.Name()[:len(".codex-rotate-untracked-auth-")] != ".codex-rotate-untracked-auth-" {
			continue
		}
		got, readErr := os.ReadFile(filepath.Join(p.ProfilesDir, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) == "unknown live credentials" {
			foundBackup = true
			break
		}
	}
	if !foundBackup {
		t.Fatal("swap did not preserve the unknown live auth.json")
	}

	wantHash, err := hashFile(p.AuthPath)
	if err != nil {
		t.Fatal(err)
	}
	if s.Profiles["other"].SHA256 != wantHash {
		t.Fatalf("other sha256 = %q, want %q", s.Profiles["other"].SHA256, wantHash)
	}
}
