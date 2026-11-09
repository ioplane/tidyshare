package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMCPManagedBoundariesAndExecutionGate(t *testing.T) {
	root, out := inventoryFixture(t)
	write(t, filepath.Join(root, "Library/Caches/Homebrew/downloads/one"), "fixture", 48*time.Hour)
	ops, err := newMCPOperations(mcpOptions{root: root, out: out})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ops.planManaged(context.Background(), mcpManagedPlanInput{Kinds: []string{"homebrew"}})
	if err != nil || len(result.PlanSHA256) != 64 {
		t.Fatalf("plan %+v %v", result, err)
	}
	planPath := filepath.Join(result.ReportDir, "managed-plan.json")
	if _, err := ops.cleanManaged(context.Background(), mcpManagedCleanInput{PlanPath: planPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.cleanManaged(context.Background(), mcpManagedCleanInput{PlanPath: planPath, Execute: true, PlanSHA256: result.PlanSHA256, Targets: []string{filepath.Join(root, "Library/Caches/Homebrew/downloads")}}); err == nil {
		t.Fatal("startup execution gate bypassed")
	}
	outside := t.TempDir()
	if _, err := ops.planManaged(context.Background(), mcpManagedPlanInput{Path: outside}); err == nil {
		t.Fatal("outside plan root accepted")
	}
	write(t, filepath.Join(outside, "Library/Caches/Homebrew/downloads/keep"), "outside", 48*time.Hour)
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: outside, Kinds: []string{"homebrew"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	forged := filepath.Join(out, "outside-plan.json")
	if err := os.WriteFile(forged, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ops.options.allowCleanup = true
	if _, err := ops.cleanManaged(context.Background(), mcpManagedCleanInput{PlanPath: forged, Execute: true, PlanSHA256: managedDigest(data), Targets: []string{filepath.Join(outside, "Library/Caches/Homebrew/downloads")}}); err == nil {
		t.Fatal("embedded outside root accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "Library/Caches/Homebrew/downloads/keep")); err != nil {
		t.Fatal("outside data changed")
	}
}
