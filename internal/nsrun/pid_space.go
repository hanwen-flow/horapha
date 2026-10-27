package nsrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/engflow/horapha/internal/criu"
	"github.com/engflow/horapha/internal/serverinfo"
)

const (
	restoreReexecArg       = "__horapha_restore_init__"
	checkpointPIDBoundName = "horapha-pid-bound.json"
	// Reject nearly exhausted namespaces instead of letting helper allocation
	// wrap into the saved task range during restore.
	restorePIDHeadroom = 65536
)

type pidBound struct {
	ServerPID int `json:"server_pid"`
	MaxID     int `json:"max_pid_tid_or_group"`
}

// namespacePIDBound runs after CRIU left the captured tree stopped. Include
// every task in this private namespace, conservatively including init/helpers,
// plus session/group IDs whose original leader may have exited. No images are read.
func namespacePIDBound(procRoot string, serverPID int) (pidBound, error) {
	bound := pidBound{ServerPID: serverPID}
	processes, err := os.ReadDir(procRoot)
	if err != nil {
		return bound, err
	}
	foundServer := false
	for _, process := range processes {
		pid, err := strconv.Atoi(process.Name())
		if err != nil || pid <= 0 {
			continue
		}
		path := filepath.Join(procRoot, process.Name())
		threads, err := os.ReadDir(filepath.Join(path, "task"))
		if os.IsNotExist(err) && pid != serverPID {
			continue // An unrelated namespace helper may have exited.
		}
		if err != nil {
			return bound, err
		}
		for _, thread := range threads {
			tid, err := strconv.Atoi(thread.Name())
			if err != nil || tid <= 0 {
				return bound, fmt.Errorf("invalid task ID %q", thread.Name())
			}
			bound.MaxID = max(bound.MaxID, tid)
		}
		stat, err := os.ReadFile(filepath.Join(path, "stat"))
		if os.IsNotExist(err) && pid != serverPID {
			continue
		}
		if err != nil {
			return bound, err
		}
		// comm can contain spaces and ')'; fields after its final ')' begin
		// with state, ppid, pgrp, session.
		end := strings.LastIndexByte(string(stat), ')')
		fields := strings.Fields(string(stat)[end+1:])
		if end < 0 || len(fields) < 4 {
			return bound, fmt.Errorf("invalid process stat for %d", pid)
		}
		if pid == serverPID && fields[0] != "T" && fields[0] != "t" {
			return bound, fmt.Errorf("checkpoint server %d is not stopped", serverPID)
		}
		for _, field := range fields[2:4] {
			id, err := strconv.Atoi(field)
			if err != nil || id < 0 {
				return bound, fmt.Errorf("invalid process group/session ID %q", field)
			}
			bound.MaxID = max(bound.MaxID, id)
		}
		foundServer = foundServer || pid == serverPID
	}
	if !foundServer || serverPID <= 1 || bound.MaxID < serverPID {
		return bound, fmt.Errorf("stopped server %d not present in namespace", serverPID)
	}
	return bound, nil
}

func writePIDBound(path string, bound pidBound) error {
	data, err := json.Marshal(bound)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func loadPIDBound(imagesDir string, pidMax int) (pidBound, error) {
	var bound pidBound
	data, err := os.ReadFile(filepath.Join(imagesDir, checkpointPIDBoundName))
	if err != nil {
		return bound, fmt.Errorf("restore requires a fresh checkpoint --stop with a PID bound: %w", err)
	}
	if err := json.Unmarshal(data, &bound); err != nil {
		return bound, fmt.Errorf("decode checkpoint PID bound: %w", err)
	}
	pid, err := os.ReadFile(filepath.Join(imagesDir, serverinfo.CheckpointPIDName))
	if err != nil {
		return bound, err
	}
	serverPID, err := strconv.Atoi(strings.TrimSpace(string(pid)))
	if err != nil || serverPID <= 1 || bound.ServerPID != serverPID || bound.MaxID < serverPID {
		return bound, fmt.Errorf("checkpoint PID bound does not match the saved server")
	}
	if pidMax <= restorePIDHeadroom || bound.MaxID >= pidMax-restorePIDHeadroom {
		return bound, fmt.Errorf("checkpoint leaves insufficient PID space for restore helpers")
	}
	return bound, nil
}

func prepareRestorePIDSpace(argv []string, reexec bool) error {
	// runInit has mounted proc for this new PID namespace. Refuse to write a
	// sysctl unless that mount actually sees us as its non-root PID 1.
	selfPID, err := os.Readlink("/proc/self")
	if err != nil || os.Getpid() != 1 || selfPID != "1" || os.Getuid() == 0 {
		return fmt.Errorf("restore PID reservation requires private proc for non-root PID 1")
	}
	outputBase := os.Getenv(OutputBaseEnv)
	if outputBase == "" {
		return fmt.Errorf("restore PID reservation requires an output base")
	}
	outputBase, err = filepath.Abs(outputBase)
	if err != nil {
		return err
	}
	imagesDir := filepath.Join(outputBase, "criu")
	pidMaxBytes, err := os.ReadFile("/proc/sys/kernel/pid_max")
	if err != nil {
		return err
	}
	pidMax, err := strconv.Atoi(strings.TrimSpace(string(pidMaxBytes)))
	if err != nil {
		return err
	}
	bound, err := loadPIDBound(imagesDir, pidMax)
	if err != nil {
		return err
	}
	if !reexec {
		if err := os.WriteFile("/proc/sys/kernel/ns_last_pid", fmt.Appendf(nil, "%d", bound.MaxID), 0); err != nil {
			return fmt.Errorf("reserve namespace-local restore PID space: %w", err)
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		// Exec removes every pre-existing low Go runtime TID except PID 1.
		// The replacement runtime and CRIU helpers are allocated above MaxID.
		args := append([]string{self, ChildArg, restoreReexecArg}, argv...)
		logControl(outputBase, "restore PID reservation: reexec_start_unix_ns=%d saved_max=%d", time.Now().UnixNano(), bound.MaxID)
		return syscall.Exec(self, args, os.Environ())
	}
	threads, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return err
	}
	for _, thread := range threads {
		tid, err := strconv.Atoi(thread.Name())
		if err != nil || (tid != 1 && tid <= bound.MaxID) {
			return fmt.Errorf("restore init still occupies a saved task ID: %s", thread.Name())
		}
	}
	logControl(outputBase, "restore PID reservation: reexec_ready_unix_ns=%d", time.Now().UnixNano())
	// Legacy CRIU resets ns_last_pid for each restored task, defeating the
	// reservation. Explicit clone3 set_tid leaves the automatic cursor intact.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	checkBinary, err := lookPath(criu.Binary())
	if err != nil {
		return err
	}
	checkBinary, err = filepath.Abs(checkBinary)
	if err != nil {
		return err
	}
	check := exec.CommandContext(ctx, checkBinary, "check", "--feature", "clone3_set_tid", "--unprivileged",
		"--log-file", filepath.Join(outputBase, "horapha-restore-check.log"))
	check.Dir = outputBase
	checkStarted := time.Now()
	output, err := check.CombinedOutput()
	logControl(outputBase, "restore PID reservation: clone3_check_seconds=%.6f success=%t", time.Since(checkStarted).Seconds(), err == nil)
	if err != nil {
		return fmt.Errorf("guarded restore requires CRIU clone3_set_tid support: %w: %s", err, output)
	}
	logControl(outputBase, "restore PID reservation: saved_max=%d pid_max=%d", bound.MaxID, pidMax)
	return nil
}
