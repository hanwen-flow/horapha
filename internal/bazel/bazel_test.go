package bazel

import (
	"slices"
	"testing"
)

func TestWithCheckpointableFlagsPrivatePerfMemory(t *testing.T) {
	args := []string{"--output_base=/private/bazel", "build", "//:target"}
	got := WithCheckpointableFlags(args)
	if !slices.Contains(StartupFlags(got), perfPrivateMemoryFlag) {
		t.Fatalf("missing private perf memory startup flag: %v", got)
	}
	if !slices.Equal(got[len(got)-len(args):], args) {
		t.Fatalf("changed original arguments: %v", got)
	}
	if again := WithCheckpointableFlags(got); !slices.Equal(again, got) {
		t.Fatalf("duplicated startup flags: %v", again)
	}
}
