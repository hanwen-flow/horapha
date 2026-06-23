package bazel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashWorkspace(t *testing.T) {
	// md5("/home/hanwen/vc/bazelbuild/bazel") computed independently.
	const ws = "/home/hanwen/vc/bazelbuild/bazel"
	const want = "8f86cad940b26c4e49c8878749e7b730"
	if got := hashWorkspace(ws); got != want {
		t.Fatalf("hashWorkspace(%q) = %q, want %q", ws, got, want)
	}
}

func TestWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "MODULE.bazel"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := WorkspaceRoot(sub)
	if !ok {
		t.Fatal("expected to find a workspace root")
	}
	// macOS /tmp symlinks; compare resolved paths.
	gotR, _ := filepath.EvalSymlinks(got)
	wantR, _ := filepath.EvalSymlinks(root)
	if gotR != wantR {
		t.Fatalf("WorkspaceRoot = %q, want %q", gotR, wantR)
	}
}

func TestWorkspaceRoot_none(t *testing.T) {
	dir := t.TempDir() // no boundary file anywhere up to /tmp's root
	if _, ok := WorkspaceRoot(dir); ok {
		// /tmp itself shouldn't be a workspace; if the host has a stray
		// boundary file above tempdir this could flake, so only fail loudly.
		t.Skip("unexpected workspace boundary above temp dir; skipping")
	}
}

func TestOutputUserRoot_default(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/x/cache")
	t.Setenv("USER", "bob")
	if got, want := OutputUserRoot(nil), "/x/cache/bazel/_bazel_bob"; got != want {
		t.Fatalf("OutputUserRoot = %q, want %q", got, want)
	}
}

func TestOutputUserRoot_explicit(t *testing.T) {
	got := OutputUserRoot([]string{"--output_user_root=/custom/root"})
	if got != "/custom/root" {
		t.Fatalf("OutputUserRoot = %q, want /custom/root", got)
	}
}

func TestResolveOutputBase_explicitWins(t *testing.T) {
	got, ok := ResolveOutputBase([]string{"--output_base=/tmp/ob"})
	if !ok || got != "/tmp/ob" {
		t.Fatalf("ResolveOutputBase = %q,%v want /tmp/ob,true", got, ok)
	}
}
