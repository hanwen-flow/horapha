package bazel

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Boundary files that mark a bazel workspace root, in the order bazel checks
// them (see WorkspaceLayout::InWorkspace in the bazel source).
var workspaceBoundaryFiles = []string{
	"MODULE.bazel", "REPO.bazel", "WORKSPACE.bazel", "WORKSPACE",
}

// ResolveOutputBase returns the output_base for an invocation, replicating
// bazel's own logic so horapha rarely needs an explicit --output_base:
//
//   - an explicit --output_base startup flag wins (tilde-expanded);
//   - otherwise --output_user_root (explicit or default) joined with the
//     md5 hex of the workspace root, exactly as the bazel client computes it.
//
// It returns ok=false only when no workspace root can be found and no explicit
// base/root was given — i.e. there is nothing bazel itself could resolve.
func ResolveOutputBase(startupFlags []string) (string, bool) {
	if ob, ok := ExplicitOutputBase(startupFlags); ok {
		return ob, true
	}

	ws, ok := WorkspaceRoot("")
	if !ok {
		return "", false
	}
	return filepath.Join(OutputUserRoot(startupFlags), hashWorkspace(ws)), true
}

// WorkspaceRoot walks up from startDir (cwd if empty) and returns the first
// directory containing a workspace boundary file, matching bazel's
// WorkspaceLayout::GetWorkspace.
func WorkspaceRoot(startDir string) (string, bool) {
	dir := startDir
	if dir == "" {
		if cwd, err := os.Getwd(); err == nil {
			dir = cwd
		} else {
			return "", false
		}
	}
	for {
		if isWorkspace(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached filesystem root
			return "", false
		}
		dir = parent
	}
}

func isWorkspace(dir string) bool {
	for _, name := range workspaceBoundaryFiles {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// OutputUserRoot returns --output_user_root if given explicitly, otherwise
// bazel's default: <cache>/bazel/_bazel_<user>, where <cache> is
// $XDG_CACHE_HOME or $HOME/.cache (see GetCacheDir / UpdateConfiguration).
func OutputUserRoot(startupFlags []string) string {
	if v, ok := unaryFlag(startupFlags, "--output_user_root"); ok {
		return expandPath(v)
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		cache = filepath.Join(homeDir(), ".cache")
	}
	return filepath.Join(cache, "bazel", "_bazel_"+userName())
}

// hashWorkspace returns the lowercase md5 hex of the workspace path, which
// bazel uses as the per-workspace output_base directory name.
func hashWorkspace(workspace string) string {
	sum := md5.Sum([]byte(workspace))
	return hex.EncodeToString(sum[:])
}

// unaryFlag returns the value of a "--flag=v" or "--flag v" startup flag.
func unaryFlag(flags []string, key string) (string, bool) {
	for i, a := range flags {
		if v, ok := strings.CutPrefix(a, key+"="); ok {
			return v, true
		}
		if a == key && i+1 < len(flags) {
			return flags[i+1], true
		}
	}
	return "", false
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

func userName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "unknown"
}
