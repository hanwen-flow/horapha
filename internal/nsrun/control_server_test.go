package nsrun

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/engflow/horapha/internal/control"
)

func TestFailedCheckpointStopRequestsTeardown(t *testing.T) {
	for _, test := range []struct {
		command string
		stop    bool
	}{
		{control.CmdCheckpoint, false},
		{control.CmdCheckpointStop, true},
	} {
		t.Run(test.command, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			outputBase := t.TempDir() // No server PID: the capture attempt fails.
			result := make(chan bool, 1)
			go func() {
				result <- handleControlConn(context.Background(), server, outputBase, nil)
			}()
			if _, err := fmt.Fprintln(client, test.command); err != nil {
				t.Fatal(err)
			}
			reply, err := bufio.NewReader(client).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(reply, "ERR read server pid:") {
				t.Fatalf("reply = %q, want capture failure", reply)
			}
			select {
			case stop := <-result:
				if stop != test.stop {
					t.Fatalf("stop namespace = %v, want %v", stop, test.stop)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("control handler did not finish")
			}
		})
	}
}

func TestCheckpointStopDisconnectCancelsDump(t *testing.T) {
	// Isolate the raw child reaper from the rest of the test process.
	if os.Getenv("HORAPHA_TEST_STOP_DISCONNECT") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCheckpointStopDisconnectCancelsDump$")
		cmd.Env = append(os.Environ(), "HORAPHA_TEST_STOP_DISCONNECT=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("reaper subprocess: %v\n%s", err, out)
		}
		return
	}

	outputBase := t.TempDir()
	if err := os.Mkdir(filepath.Join(outputBase, "server"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputBase, "server", "server.pid.txt"), []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(outputBase, "fake-criu")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec sleep 6\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HORAPHA_CRIU", binary)
	w := commandWaiters{outputBase: outputBase, pending: make(map[int]commandWaiter)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, client := net.Pipe()
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	result := make(chan bool, 1)
	go func() {
		result <- handleControlConn(ctx, server, outputBase, &w)
	}()
	if _, err := fmt.Fprintln(client, control.CmdCheckpointStop); err != nil {
		t.Fatal(err)
	}
	var pid int
	for pid == 0 {
		w.mu.Lock()
		for child := range w.pending {
			pid = child
		}
		w.mu.Unlock()
		if ctx.Err() != nil {
			t.Fatal("dump child did not start before the outer timeout")
		}
		if pid == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	client.Close()
	var status syscall.WaitStatus
	var usage syscall.Rusage
	for {
		_, err := syscall.Wait4(pid, &status, 0, &usage)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	w.reaped(pid, status, usage)
	select {
	case stop := <-result:
		if !stop || !status.Signaled() || status.Signal() != syscall.SIGKILL || ctx.Err() != nil {
			t.Fatalf("stop=%t, child status=%v, outer timeout=%v; want disconnect-driven teardown", stop, status, ctx.Err())
		}
	case <-ctx.Done():
		t.Fatal("disconnected control handler did not finish")
	}
}
