package criu

import (
	"slices"
	"testing"
)

func TestDumpFinalState(t *testing.T) {
	for _, leaveRunning := range []bool{false, true} {
		args := DumpArgs(42, Options{LeaveRunning: leaveRunning})
		if got := slices.Contains(args, "--leave-running"); got != leaveRunning {
			t.Errorf("LeaveRunning=%t: --leave-running present=%t", leaveRunning, got)
		}
		if got := slices.Contains(args, "--leave-stopped"); got == leaveRunning {
			t.Errorf("LeaveRunning=%t: --leave-stopped present=%t", leaveRunning, got)
		}
	}
}
