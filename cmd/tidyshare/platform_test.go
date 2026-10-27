package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRenamePreservesExistingDestination(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(unix.AT_FDCWD, src, unix.AT_FDCWD, dst); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing destination refusal, got %v", err)
	}
	for p, want := range map[string]string{src: "source", dst: "destination"} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Fatalf("%s changed: %q %v", p, got, err)
		}
	}
}

func TestDarwinDedupeRefusesBeforeManifestWrites(t *testing.T) {
	if hostPlatform() != "darwin" {
		t.Skip("Darwin capability refusal")
	}
	_, dup, plan, q := applyFixture(t)
	if code := runApply([]string{"-plan", plan, "-quarantine", q, "-mode", "dedupe", "-yes"}); code == 0 {
		t.Fatal("unsupported dedupe reported success")
	}
	if _, err := os.Stat(filepath.Join(q, "MANIFEST.tsv")); !os.IsNotExist(err) {
		t.Fatal("unsupported operation wrote a manifest")
	}
	if b, err := os.ReadFile(dup); err != nil || string(b) != "same-content" {
		t.Fatal("unsupported operation changed duplicate")
	}
}

func TestDarwinDuplicateCleanupPreservesResourceFork(t *testing.T) {
	if hostPlatform() != "darwin" {
		t.Skip("Darwin resource forks")
	}
	_, dup, plan, _ := applyFixture(t)
	info, err := os.Stat(dup)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(dup, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = unix.Fsetxattr(int(f.Fd()), "com.apple.ResourceFork", []byte("unique resource fixture"), 0)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dup, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	report, err := cleanupDuplicates(context.Background(), plan, true, 1)
	if err != nil || report.Changed != 0 {
		t.Fatalf("resource fork deleted: %+v %v", report, err)
	}
	if _, err := os.Stat(dup); err != nil {
		t.Fatal("resource fork file removed")
	}
}
