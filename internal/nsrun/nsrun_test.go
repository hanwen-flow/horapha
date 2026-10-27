package nsrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestWaitClient(t *testing.T) {
	if mode := os.Getenv("HORAPHA_TEST_WAIT_CLIENT"); mode != "" {
		signals := make(chan os.Signal, 1)
		if mode == "ignore" {
			signal.Ignore(os.Interrupt)
		} else {
			signal.Notify(signals, os.Interrupt)
		}
		status := os.NewFile(3, "status")
		ready := os.NewFile(4, "ready")
		_, _ = ready.Write([]byte{1})
		_ = ready.Close()
		if mode == "ignore" {
			for {
				time.Sleep(time.Hour)
			}
		}
		code := byte(0)
		if mode == "cancel" {
			<-signals
			code = 130
		}
		_, _ = status.Write([]byte{code})
		_ = status.Close()
		// Model init retaining the daemon after its initial client exits.
		<-signals
		os.Exit(42)
	}

	for _, mode := range []string{"cancel", "ignore", "complete"} {
		t.Run(mode, func(t *testing.T) {
			status, statusWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer status.Close()
			defer statusWrite.Close()
			ready, readyWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer ready.Close()
			defer readyWrite.Close()
			limit, stop := context.WithTimeout(context.Background(), 3*interruptGrace)
			defer stop()
			cmd := exec.CommandContext(limit, os.Args[0], "-test.run=^TestWaitClient$")
			cmd.Env = append(os.Environ(), "HORAPHA_TEST_WAIT_CLIENT="+mode)
			cmd.ExtraFiles = []*os.File{statusWrite, readyWrite}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			var waitErr error
			go func() {
				waitErr = cmd.Wait()
				close(exited)
			}()
			defer func() {
				_ = cmd.Process.Kill()
				<-exited
			}()
			_ = statusWrite.Close()
			_ = readyWrite.Close()
			var b [1]byte
			if _, err := ready.Read(b[:]); err != nil {
				t.Fatalf("wait for signal handler: %v", err)
			}
			ctx, cancel := context.WithCancel(limit)
			defer cancel()
			if mode != "complete" {
				cancel()
			}
			started := time.Now()
			err = waitClient(ctx, cmd.Process, status)
			if mode != "complete" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v, want context.Canceled", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "ignore" {
				select {
				case <-exited:
				case <-time.After(time.Second):
					t.Fatal("interrupt-ignoring init survived cancellation grace")
				}
				var exit *exec.ExitError
				if !errors.As(waitErr, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatalf("init exit = %v, want SIGKILL", waitErr)
				}
				if elapsed := time.Since(started); elapsed < interruptGrace || limit.Err() != nil {
					t.Fatalf("cleanup took %s with outer deadline %v; want our grace timer", elapsed, limit.Err())
				}
				return
			}
			cancel()
			select {
			case <-exited:
				t.Fatalf("init exited after client status: %v", waitErr)
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}

func TestRunAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, "", []string{"/bin/true"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled without starting a namespace", err)
	}
}
