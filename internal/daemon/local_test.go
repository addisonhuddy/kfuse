// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

func TestLocalStatePaths(t *testing.T) {
	if got, want := SelectedPath("/state", "low1"), "/state/low1/selected"; got != want {
		t.Errorf("SelectedPath = %q, want %q", got, want)
	}
	if got, want := ControlSocketPath("/state", "low1", "sess1"), "/state/low1/sess1/control.sock"; got != want {
		t.Errorf("ControlSocketPath = %q, want %q", got, want)
	}
}

// The kernel rejects a bind on an over-long sockaddr_un path, so the socket
// path must fit whatever state dir the caller has (macOS temp dirs are deep).
func TestControlSocketPathFitsPlatformLimit(t *testing.T) {
	for _, stateDir := range []string{
		"/state",
		t.TempDir(),
		filepath.Join("/var/folders/4w", strings.Repeat("x", 40), "T", strings.Repeat("deep", 30)),
	} {
		path := ControlSocketPath(stateDir, "lower-0123456789abcdef", "sess-0123456789abcdef")
		if len(path) >= maxUnixSocketPath {
			t.Errorf("ControlSocketPath(%q) = %q (%d bytes), want under %d", stateDir, path, len(path), maxUnixSocketPath)
		}
		// Every process must derive the same path for one session.
		if again := ControlSocketPath(stateDir, "lower-0123456789abcdef", "sess-0123456789abcdef"); again != path {
			t.Errorf("ControlSocketPath is not deterministic: %q then %q", path, again)
		}
	}
}

// Distinct sessions sharing the fallback directory must not collide.
func TestControlSocketPathFallbackIsPerSession(t *testing.T) {
	long := filepath.Join("/tmp", strings.Repeat("deep", 30))
	a := ControlSocketPath(long, "low1", "sess1")
	b := ControlSocketPath(long, "low1", "sess2")
	c := ControlSocketPath(long, "low2", "sess1")
	if a == b || a == c || b == c {
		t.Errorf("fallback paths collide: %q %q %q", a, b, c)
	}
}

func TestSelectedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadSelected(dir, "low1")
	if err != nil {
		t.Fatalf("ReadSelected with no marker = %v, want no error", err)
	}
	if got != "" {
		t.Errorf("ReadSelected with no marker = %q, want empty", got)
	}
	if err := WriteSelected(dir, "low1", "sess1"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSelected(dir, "low1"); err != nil || got != "sess1" {
		t.Errorf("ReadSelected = (%q, %v), want sess1 (trailing newline trimmed)", got, err)
	}
	// Selecting again must replace, not append.
	if err := WriteSelected(dir, "low1", "sess2"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSelected(dir, "low1"); err != nil || got != "sess2" {
		t.Errorf("ReadSelected after reselect = (%q, %v), want sess2", got, err)
	}
}

// An unreadable marker must not read as "no session selected": that would
// mount a fresh session over a lower that already has one.
func TestReadSelectedUnreadableMarkerIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	if err := WriteSelected(dir, "low1", "sess1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(SelectedPath(dir, "low1"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSelected(dir, "low1"); err == nil {
		t.Fatal("ReadSelected on an unreadable marker must fail")
	}
}

func TestPidFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "pid")
	if err := WritePidFile(path, 4242); err != nil {
		t.Fatal(err)
	}
	pid, err := ReadPidFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 4242 {
		t.Errorf("ReadPidFile = %d, want 4242", pid)
	}

	if _, err := ReadPidFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("ReadPidFile on a missing file must fail")
	}
	garbage := filepath.Join(t.TempDir(), "pid")
	if err := os.WriteFile(garbage, []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPidFile(garbage); err == nil {
		t.Error("ReadPidFile on a non-numeric pidfile must fail")
	}
}

func TestIsAlive(t *testing.T) {
	if !IsAlive(os.Getpid()) {
		t.Error("IsAlive(self) = false, want true")
	}
	if IsAlive(0) || IsAlive(-1) {
		t.Error("IsAlive must reject non-positive pids")
	}
	// Highest possible pid+1 is never running.
	if IsAlive(1 << 30) {
		t.Error("IsAlive(unused pid) = true, want false")
	}
}

func TestPidFilePathIsPerSession(t *testing.T) {
	s := &Stack{Config: config.Config{StateDir: "/state"}}
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, upper.New(), nil, nil, nil)
	if got, want := s.PidFilePath(sess), "/state/low1/sess1/pid"; got != want {
		t.Errorf("PidFilePath = %q, want %q", got, want)
	}
}

func TestResolveLowerPrefersOverrideThenEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KF_LOWER_ID", "from-env")

	abs, id, err := ResolveLower(dir, "from-flag")
	if err != nil {
		t.Fatal(err)
	}
	if abs != dir {
		t.Errorf("abs = %q, want %q", abs, dir)
	}
	if id != "from-flag" {
		t.Errorf("id = %q, want the flag override", id)
	}

	if _, id, err = ResolveLower(dir, ""); err != nil {
		t.Fatal(err)
	} else if id != "from-env" {
		t.Errorf("id = %q, want the env value", id)
	}
	// Neither path may write a marker: the ID came from the caller.
	if _, err := os.Stat(filepath.Join(dir, ".kf-lower-id")); !os.IsNotExist(err) {
		t.Errorf("marker must not be written when the id is supplied (err=%v)", err)
	}
}

func TestResolveLowerReadsMarker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KF_LOWER_ID", "")
	if err := os.WriteFile(filepath.Join(dir, ".kf-lower-id"), []byte(" from-marker \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, id, err := ResolveLower(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if id != "from-marker" {
		t.Errorf("id = %q, want from-marker (whitespace trimmed)", id)
	}
}

// Minting a replacement id would silently point the mount at an empty session
// namespace and overwrite the existing marker.
func TestResolveLowerUnreadableMarkerIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	t.Setenv("KF_LOWER_ID", "")
	marker := filepath.Join(dir, ".kf-lower-id")
	if err := os.WriteFile(marker, []byte("lower-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveLower(dir, ""); err == nil {
		t.Fatal("ResolveLower with an unreadable marker must fail")
	}
	if err := os.Chmod(marker, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "lower-existing" {
		t.Fatalf("marker = (%q, %v), want it left untouched", b, err)
	}
}

func TestResolveLowerMintsAndPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KF_LOWER_ID", "")

	_, id, err := ResolveLower(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "lower-") || len(id) != len("lower-")+16 {
		t.Fatalf("minted id = %q, want lower- plus 16 hex chars", id)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".kf-lower-id"))
	if err != nil {
		t.Fatalf("minted id must be persisted: %v", err)
	}
	if strings.TrimSpace(string(b)) != id {
		t.Errorf("marker = %q, want %q", b, id)
	}
	// A second resolve must reuse the persisted id, so a remount keeps the
	// same lower identity (and therefore the same sessions).
	if _, id2, err := ResolveLower(dir, ""); err != nil {
		t.Fatal(err)
	} else if id2 != id {
		t.Errorf("second resolve = %q, want the persisted %q", id2, id)
	}
}

func TestResolveLowerRelativePathBecomesAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("KF_LOWER_ID", "low1")
	abs, _, err := ResolveLower(".", "")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(abs) {
		t.Errorf("abs = %q, want an absolute path", abs)
	}
}

func TestControlServerStatusAndUnknownCommand(t *testing.T) {
	s := &Stack{Config: config.Config{StateDir: t.TempDir()}}
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, upper.New(), nil, nil, nil)
	stop, err := s.StartControlServer(sess)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	sock := ControlSocketPath(s.Config.StateDir, "low1", "sess1")
	reply, err := ControlRequest(sock, "status")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "status sess1 0" {
		t.Errorf("status reply = %q, want %q", reply, "status sess1 0")
	}

	reply, err = ControlRequest(sock, "bogus")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply, "error unknown command") {
		t.Errorf("unknown command reply = %q, want an error", reply)
	}
}

// A state dir deep enough to blow the sockaddr_un limit must still serve, and
// the client must reach it from the same path derivation.
func TestControlServerWithOverlongStateDir(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), strings.Repeat("deep", 30))
	s := &Stack{Config: config.Config{StateDir: stateDir}}
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, upper.New(), nil, nil, nil)
	stop, err := s.StartControlServer(sess)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	reply, err := ControlRequest(ControlSocketPath(stateDir, "low1", "sess1"), "status")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "status sess1 0" {
		t.Errorf("status reply = %q, want %q", reply, "status sess1 0")
	}
}

func TestControlServerStopRemovesSocket(t *testing.T) {
	s := &Stack{Config: config.Config{StateDir: t.TempDir()}}
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, upper.New(), nil, nil, nil)
	stop, err := s.StartControlServer(sess)
	if err != nil {
		t.Fatal(err)
	}
	sock := ControlSocketPath(s.Config.StateDir, "low1", "sess1")

	// A stale socket must not block a restart.
	stop()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket must be removed on stop (err=%v)", err)
	}
	stop2, err := s.StartControlServer(sess)
	if err != nil {
		t.Fatalf("restart after stop: %v", err)
	}
	stop2()
}

func TestControlRequestOnMissingSocket(t *testing.T) {
	if _, err := ControlRequest(filepath.Join(t.TempDir(), "control.sock"), "status"); err == nil {
		t.Fatal("ControlRequest to a nonexistent socket must fail")
	}
}
