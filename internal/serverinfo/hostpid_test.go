package serverinfo

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestHostPIDForNSPIDRequiresMatchingNamespace(t *testing.T) {
	namespace, err := os.Stat("/proc/self/ns/pid")
	if err != nil {
		t.Fatal(err)
	}
	nsPID, ok := innerNSPID(os.Getpid())
	if !ok {
		t.Fatal("cannot read this process's namespace PID")
	}
	pid, err := HostPIDForNSPID(nsPID, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Fatalf("host PID = %d, want %d", pid, os.Getpid())
	}
	otherNamespace, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := HostPIDForNSPID(nsPID, otherNamespace); err == nil {
		t.Fatal("resolved a PID outside the specified namespace")
	}
}

func TestWaitHostPIDForNSPIDCancellation(t *testing.T) {
	namespace, err := os.Stat(t.TempDir()) // Cannot match a process namespace.
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := WaitHostPIDForNSPID(ctx, 1, namespace, "java", 5*time.Second)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("lookup returned before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lookup error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lookup ignored cancellation")
	}
}
