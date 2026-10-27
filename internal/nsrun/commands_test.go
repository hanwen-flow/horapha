package nsrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/engflow/horapha/internal/bazel"
)

func TestCommandStatusFromInitReaper(t *testing.T) {
	// Isolate Wait4(-1) so this test cannot reap another test's children.
	if os.Getenv("HORAPHA_TEST_INIT_REAPER") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandStatusFromInitReaper$")
		cmd.Env = append(os.Environ(), "HORAPHA_TEST_INIT_REAPER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("reaper subprocess: %v\n%s", err, out)
		}
		return
	}

	outputBase := t.TempDir()
	binary := filepath.Join(outputBase, "criu")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HORAPHA_CRIU", binary)
	w := commandWaiters{outputBase: outputBase, pending: make(map[int]commandWaiter)}
	result := make(chan error, 1)
	go func() {
		result <- w.run(context.Background(), []string{binary, "dump"})
	}()

	var status syscall.WaitStatus
	var usage syscall.Rusage
	var pid int
	for {
		var err error
		pid, err = syscall.Wait4(-1, &status, 0, &usage)
		if err == syscall.ECHILD || err == syscall.EINTR {
			time.Sleep(time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	w.reaped(pid, status, usage)
	var exit *bazel.ExitError
	if err := <-result; !errors.As(err, &exit) || exit.Code != 23 {
		t.Fatalf("exit status from init: got %v, want exit 23", err)
	}
	log, err := os.ReadFile(filepath.Join(outputBase, "horapha-init.log"))
	if err != nil {
		t.Fatal(err)
	}
	var metrics struct {
		Operation   string  `json:"operation"`
		Scope       string  `json:"scope"`
		PID         int     `json:"namespace_pid"`
		ExitCode    int     `json:"exit_code"`
		WallSeconds float64 `json:"wall_seconds"`
		MinorFaults int64   `json:"minor_faults"`
		MaxRSSKiB   int64   `json:"max_rss_kib"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(string(log), "horapha-init: CRIU_RUSAGE ")), &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Operation != "dump" || metrics.Scope != "waited_command_and_reaped_children" ||
		metrics.PID != pid || metrics.ExitCode != 23 || metrics.WallSeconds <= 0 ||
		metrics.MinorFaults != usage.Minflt || metrics.MaxRSSKiB != usage.Maxrss {
		t.Fatalf("incorrect reaped-command metrics: %+v; rusage: %+v", metrics, usage)
	}
}
