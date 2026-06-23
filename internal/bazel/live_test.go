package bazel

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestResolveMatchesBazel compares our derivation to `bazel info output_base`
// in the workspace named by HORAPHA_TEST_WORKSPACE. Skips unless both
// HORAPHA_BAZEL and HORAPHA_TEST_WORKSPACE are set.
func TestResolveMatchesBazel(t *testing.T) {
	bin := os.Getenv("HORAPHA_BAZEL")
	ws := os.Getenv("HORAPHA_TEST_WORKSPACE")
	if bin == "" || ws == "" {
		t.Skip("set HORAPHA_BAZEL and HORAPHA_TEST_WORKSPACE to run")
	}
	cmd := exec.Command(bin, "info", "output_base")
	cmd.Dir = ws
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("bazel info failed: %v", err)
	}
	want := strings.TrimSpace(string(out))

	root, ok := WorkspaceRoot(ws)
	if !ok {
		t.Fatalf("no workspace root at %s", ws)
	}
	got := joinOutputBase(OutputUserRoot(nil), root)
	if got != want {
		t.Fatalf("derived output_base mismatch:\n got: %s\nwant: %s", got, want)
	}
	t.Logf("match: %s", got)
}

func joinOutputBase(userRoot, ws string) string {
	return userRoot + "/" + hashWorkspace(ws)
}
