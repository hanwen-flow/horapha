package serverinfo

import (
	"os"
	"path/filepath"
	"testing"
)

// realRawproto is the first bytes of an actual server_info.rawproto captured
// from bazel 7.4.1: field 1 (pid) varint = 11, then field 2 (address)
// "[::1]:46189", then field 3 (request_cookie)...
var realRawproto = []byte{
	0x08, 0x0b, // field 1 (pid) = 11
	0x12, 0x0b, '[', ':', ':', '1', ']', ':', '4', '6', '1', '8', '9', // field 2 address
	0x1a, 0x04, 'a', 'b', 'c', 'd', // field 3 (truncated cookie) — just to have trailing bytes
}

func TestPatchRawprotoPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RawprotoName)
	if err := os.WriteFile(path, realRawproto, 0o644); err != nil {
		t.Fatal(err)
	}

	// Patch ns-local 11 -> a large host pid that needs a multi-byte varint.
	const hostPID = 1005
	if err := PatchRawprotoPID(path, hostPID); err != nil {
		t.Fatalf("patch: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Expect: 0x08, varint(1005)=0xed 0x07, then the original bytes from index 2.
	want := append([]byte{0x08, 0xed, 0x07}, realRawproto[2:]...)
	if string(got) != string(want) {
		t.Fatalf("patched bytes mismatch\n got: % x\nwant: % x", got, want)
	}

	// Idempotence of structure: patching again to the same pid is stable.
	if err := PatchRawprotoPID(path, hostPID); err != nil {
		t.Fatalf("re-patch: %v", err)
	}
	got2, _ := os.ReadFile(path)
	if string(got2) != string(want) {
		t.Fatalf("re-patch changed bytes\n got: % x\nwant: % x", got2, want)
	}
}

func TestPatchRawprotoPID_rejectsNonPidLeadByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RawprotoName)
	// Leading byte 0x12 == field 2, not the pid field.
	if err := os.WriteFile(path, []byte{0x12, 0x01, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PatchRawprotoPID(path, 7); err == nil {
		t.Fatal("expected error for non-pid leading field, got nil")
	}
}

func TestReadStartTime(t *testing.T) {
	// Field 22 (1-based) of /proc/self/stat; just assert we get a positive
	// integer-looking value and that it's stable across two reads.
	got, err := ReadStartTime(os.Getpid())
	if err != nil {
		t.Fatalf("ReadStartTime: %v", err)
	}
	if got == "" {
		t.Fatal("empty start time")
	}
	if _, err := ParsePID(got); err != nil {
		t.Fatalf("start time %q not numeric: %v", got, err)
	}
	again, _ := ReadStartTime(os.Getpid())
	if got != again {
		t.Fatalf("start time not stable: %q vs %q", got, again)
	}
}
