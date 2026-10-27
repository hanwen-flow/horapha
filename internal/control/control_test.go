package control

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestPeerPID(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("unix", SockPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pid, err := PeerPID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Fatalf("peer PID = %d, want %d", pid, os.Getpid())
	}
}

func TestRequestCancellationWhileWaiting(t *testing.T) {
	dir := t.TempDir()
	listener, err := net.Listen("unix", SockPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := Request(ctx, dir, CmdCheckpoint)
		result <- err
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	command, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || command != CmdCheckpoint+"\n" {
		t.Fatalf("request = %q, %v", command, err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request remained blocked after cancellation")
	}
}
