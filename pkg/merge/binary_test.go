package merge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBinaryMerge(t *testing.T) {
	dir := t.TempDir()

	// Create test files
	f1 := filepath.Join(dir, "a.ts")
	f2 := filepath.Join(dir, "b.ts")
	f3 := filepath.Join(dir, "c.ts")

	os.WriteFile(f1, []byte("AAA"), 0644)
	os.WriteFile(f2, []byte("BBB"), 0644)
	os.WriteFile(f3, []byte("CCC"), 0644)

	out := filepath.Join(dir, "merged.ts")
	err := BinaryMerge([]string{f1, f2, f3}, out)
	if err != nil {
		t.Fatalf("BinaryMerge: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := "AAABBBCCC"
	if string(data) != want {
		t.Errorf("merged content = %q, want %q", data, want)
	}
}

func TestBinaryMergeEmpty(t *testing.T) {
	err := BinaryMerge(nil, "/tmp/test.ts")
	if err == nil {
		t.Error("expected error for empty paths")
	}
}

func TestBinaryMergeEmptyPath(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.ts")
	os.WriteFile(f1, []byte("AAA"), 0644)

	out := filepath.Join(dir, "merged.ts")
	err := BinaryMerge([]string{f1, "", f1}, out)
	if err == nil {
		t.Error("expected error for empty path at index 1")
	}
}

func TestBinaryMergeWithInit(t *testing.T) {
	dir := t.TempDir()

	init := filepath.Join(dir, "init.mp4")
	seg1 := filepath.Join(dir, "seg1.m4s")
	seg2 := filepath.Join(dir, "seg2.m4s")

	os.WriteFile(init, []byte("INIT"), 0644)
	os.WriteFile(seg1, []byte("SEG1"), 0644)
	os.WriteFile(seg2, []byte("SEG2"), 0644)

	out := filepath.Join(dir, "output.mp4")
	err := BinaryMergeWithInit(init, []string{seg1, seg2}, out)
	if err != nil {
		t.Fatalf("BinaryMergeWithInit: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := "INITSEG1SEG2"
	if string(data) != want {
		t.Errorf("merged content = %q, want %q", data, want)
	}
}

func TestBinaryMergeWithNoInit(t *testing.T) {
	dir := t.TempDir()
	seg1 := filepath.Join(dir, "seg1.ts")
	os.WriteFile(seg1, []byte("DATA"), 0644)

	out := filepath.Join(dir, "output.ts")
	err := BinaryMergeWithInit("", []string{seg1}, out)
	if err != nil {
		t.Fatalf("BinaryMergeWithInit (no init): %v", err)
	}

	data, _ := os.ReadFile(out)
	if string(data) != "DATA" {
		t.Errorf("content = %q, want DATA", data)
	}
}
