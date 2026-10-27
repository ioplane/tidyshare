package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Removing per-input scope validation must admit an outside project or previous report.
func TestMCPWorkstationBoundaries(t *testing.T) {
	root, out := inventoryFixture(t)
	ops, err := newMCPOperations(mcpOptions{root: root, out: out})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	for _, in := range []mcpWorkstationInput{{ProjectRoots: []string{outside}}, {Previous: filepath.Join(outside, "report.json")}} {
		if _, err := ops.inventoryWorkstation(context.Background(), in); err == nil {
			t.Fatal("outside input accepted")
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.inventoryWorkstation(context.Background(), mcpWorkstationInput{ProjectRoots: []string{filepath.Join(root, "alias")}}); err == nil {
		t.Fatal("symlink project accepted")
	}
	write(t, filepath.Join(root, ".npm/_cacache/data"), "fixture", time.Hour)
	result, err := ops.inventoryWorkstation(context.Background(), mcpWorkstationInput{})
	if err != nil {
		t.Fatal(err)
	}
	r := readWorkstationFixture(t, result.ReportDir)
	if !r.Complete || result.Execute || len(result.Reports) != 2 || r.Runtime.State != "not_requested" {
		t.Fatalf("result %+v %+v", result, r)
	}
	if _, err := ops.inventoryWorkstation(context.Background(), mcpWorkstationInput{Previous: filepath.Join(result.ReportDir, "workstation.json")}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(root); _ = os.Rename(root+"-old", root) })
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.inventoryWorkstation(context.Background(), mcpWorkstationInput{}); err == nil {
		t.Fatal("replaced anchor accepted")
	}
}

func TestWorkstationRefusesReportsInsideCustomOrChromeCache(t *testing.T) {
	root, _ := inventoryFixture(t)
	for _, suffix := range []string{"custom-cache", "Library/Caches/Google/Chrome"} {
		cache := filepath.Join(root, suffix)
		write(t, filepath.Join(cache, "00", "fixture"), "payload", time.Hour)
		t.Setenv("GOCACHE", cache)
		if code := runWorkstation([]string{"-root", root, "-out", filepath.Join(cache, "out")}); code == 0 {
			t.Fatal("report output inside measured cache accepted")
		}
		if _, err := os.Lstat(filepath.Join(cache, "out")); !os.IsNotExist(err) {
			t.Fatal("unsafe report directory created")
		}
	}
}
