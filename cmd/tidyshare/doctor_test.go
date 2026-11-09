package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanDistinguishesSparseAllocation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sparse.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	es, err := walk(opts{root: root, staleDays: 180})
	if err != nil || len(es) != 1 || es[0].Size != 64<<20 || es[0].Allocated >= es[0].Size {
		t.Fatalf("sparse allocation lost: %+v %v", es, err)
	}
}

func TestDoctorReportsUnsupportedEngineWithoutMutations(t *testing.T) {
	root := t.TempDir()
	r := inspectCapabilities(context.Background(), root, "invalid", "")
	if r.Runtime.Status != "unsupported" || r.Filesystem.FreeBytes == nil {
		t.Fatalf("capabilities: %+v", r)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("doctor changed data")
	}
}

func TestCachePlanRejectsProtectedTargetAndForeignPlatform(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, ".cache", "go-build")
	write(t, filepath.Join(cache, "00", "payload"), "cache", time.Hour)
	target, err := cacheTargetAt(context.Background(), cache)
	if err != nil || target == nil {
		t.Fatal(target, err)
	}
	p := cachePlan{SchemaVersion: 2, Platform: "foreign", Root: root, Targets: []cacheTarget{*target}}
	if validateCachePlan(p) == nil {
		t.Fatal("foreign platform accepted")
	}
	p.Platform = hostPlatform()
	p.Policy = storagePolicy{Protected: []string{cache}}
	if validateCachePlan(p) == nil {
		t.Fatal("protected target accepted")
	}
	p.Policy = storagePolicy{}
	if err := validateCachePlan(p); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var restored cachePlan
	if err := decodeStructuredPlan(body, &restored); err != nil {
		t.Fatal(err)
	}
	if err := validateCachePlan(restored); err != nil {
		t.Fatal(err)
	}
}

func TestScanRecordsIncompleteCoverage(t *testing.T) {
	root := t.TempDir()
	unreadable := filepath.Join(root, "unreadable")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(unreadable, 0o700) }()
	if os.Geteuid() == 0 {
		t.Skip("root can read the permission fixture")
	}
	var warnings []scanWarning
	_, err := walk(opts{root: root, warnings: &warnings})
	if err != nil || len(warnings) == 0 {
		t.Fatalf("missing coverage warnings: %+v %v", warnings, err)
	}
}

func TestDoctorRelativeRootTerminates(t *testing.T) {
	done := make(chan capabilityReport, 1)
	go func() { done <- inspectCapabilities(context.Background(), ".", "invalid", "") }()
	select {
	case report := <-done:
		if report.Filesystem.FreeBytes == nil {
			t.Fatalf("relative root unavailable: %+v", report.Filesystem)
		}
	case <-time.After(time.Second):
		t.Fatal("doctor hangs on relative root")
	}
}
