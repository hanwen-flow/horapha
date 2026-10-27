package nsrun

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/engflow/horapha/internal/serverinfo"
)

func TestNamespacePIDBoundIncludesThreadsAndGroups(t *testing.T) {
	proc := t.TempDir()
	for _, process := range []struct {
		pid, group, session int
		threads             []int
	}{
		{1, 0, 0, []int{1, 9}},
		{12, 1800, 1700, []int{12, 1400}},
	} {
		path := filepath.Join(proc, fmt.Sprint(process.pid))
		for _, tid := range process.threads {
			if err := os.MkdirAll(filepath.Join(path, "task", fmt.Sprint(tid)), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		stat := fmt.Sprintf("%d (name with ) and spaces) T 1 %d %d 0\n", process.pid, process.group, process.session)
		if err := os.WriteFile(filepath.Join(path, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A helper disappearing during enumeration must not hide the stopped server.
	if err := os.Mkdir(filepath.Join(proc, "1900"), 0o700); err != nil {
		t.Fatal(err)
	}
	bound, err := namespacePIDBound(proc, 12)
	if err != nil {
		t.Fatal(err)
	}
	if bound != (pidBound{ServerPID: 12, MaxID: 1800}) {
		t.Fatalf("got %+v", bound)
	}
	if _, err := namespacePIDBound(proc, 13); err == nil {
		t.Fatal("accepted an absent saved server")
	}
	if err := os.WriteFile(filepath.Join(proc, "12", "stat"), []byte("12 (java) S 1 1800 1700\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := namespacePIDBound(proc, 12); err == nil || !strings.Contains(err.Error(), "not stopped") {
		t.Fatalf("accepted a running server: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(proc, "12", "task")); err != nil {
		t.Fatal(err)
	}
	if _, err := namespacePIDBound(proc, 12); err == nil {
		t.Fatal("ignored loss of the saved server's tasks")
	}
}

func TestLoadPIDBoundRejectsUnsafeRestore(t *testing.T) {
	images := t.TempDir() // Synthetic metadata only; no CRIU images.
	if _, err := loadPIDBound(images, 4194304); err == nil || !strings.Contains(err.Error(), "checkpoint --stop") {
		t.Fatalf("missing bound: %v", err)
	}
	if err := os.WriteFile(filepath.Join(images, serverinfo.CheckpointPIDName), []byte("12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		bound  pidBound
		pidMax int
		valid  bool
	}{
		{"valid", pidBound{12, 1800}, 4194304, true},
		{"wrong-server", pidBound{13, 1800}, 4194304, false},
		{"below-server", pidBound{12, 11}, 4194304, false},
		{"no-headroom", pidBound{12, 4194304 - restorePIDHeadroom}, 4194304, false},
		{"small-pid-max", pidBound{12, 1800}, 32768, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := writePIDBound(filepath.Join(images, checkpointPIDBoundName), test.bound); err != nil {
				t.Fatal(err)
			}
			got, err := loadPIDBound(images, test.pidMax)
			if (err == nil) != test.valid || (err == nil && got != test.bound) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestChildRefusesHostBeforeMountOrPIDWrite(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("test requires a non-init process")
	}
	for _, argv := range [][]string{{"/bin/true"}, {restoreReexecArg, "/bin/true"}} {
		if code, err := Child(argv); code != 1 || err == nil || !strings.Contains(err.Error(), "non-root PID 1") {
			t.Fatalf("Child(%q) = %d, %v", argv, code, err)
		}
	}
}
