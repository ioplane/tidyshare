package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProtectedRootsOverrideCustomCache(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "custom-cache")
	write(t, filepath.Join(cache, "00", "payload"), "cache", time.Hour)
	t.Setenv("GOCACHE", cache)
	p, err := newStoragePolicy("", []string{cache})
	if err != nil {
		t.Fatal(err)
	}
	report, err := discoverStorageWithPolicy(context.Background(), root, filepath.Join(root, "out"), p)
	if err != nil || len(report.Caches) != 0 {
		t.Fatalf("protected cache discovered: %+v %v", report, err)
	}
}

func TestWorkstationProtectsOrbStackViewAndBackingStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	p, err := newStoragePolicy("workstation", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"OrbStack", "Library/Group Containers/HUAQ24HBR6.dev.orbstack/data", ".codex", ".ollama"} {
		path := filepath.Join(root, dir, "payload")
		if !p.excludes(path) {
			t.Errorf("unprotected path: %s", path)
		}
	}
	if p.excludes(filepath.Join(root, "project", "source.go")) {
		t.Fatal("source directory excluded")
	}
}

func TestMacCacheLayouts(t *testing.T) {
	if got := cacheKind("/Users/fixture/Library/Caches/go-build", true); got != "go_build" {
		t.Fatalf("Go cache: %q", got)
	}
	if got := cacheKind("/Users/fixture/Library/Caches/pip", true); got != "pip" {
		t.Fatalf("pip cache: %q", got)
	}
}

func TestPrivateTemporaryDirectoryResolvesSystemAlias(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", alias)
	path, err := privateTemp("test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path)
	if filepath.Dir(path) != real {
		t.Fatalf("temporary path retains alias: %s", path)
	}
}

func TestMCPRefusesProtectedCleanupPlan(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	protected := filepath.Join(root, "protected")
	write(t, filepath.Join(protected, "keep"), "same", 48*time.Hour)
	write(t, filepath.Join(protected, "dup"), "same", 48*time.Hour)
	plan := filepath.Join(out, "dups.tsv")
	st, err := os.Stat(filepath.Join(protected, "keep"))
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("group\taction\tsize\tmtime\tpath\n1\tkeep\t4\t%d\t%q\n1\tdup\t4\t%d\t%q\n", st.ModTime().Unix(), filepath.Join(protected, "keep"), st.ModTime().Unix(), filepath.Join(protected, "dup"))
	if err := os.WriteFile(plan, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ops, err := newMCPOperations(mcpOptions{root: root, out: out, allowCleanup: true, policy: storagePolicy{Protected: []string{protected}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.cleanup(context.Background(), "duplicates", mcpCleanupInput{PlanPath: plan, Execute: true}); err == nil {
		t.Fatal("protected duplicate cleanup accepted")
	}
	if b, err := os.ReadFile(filepath.Join(protected, "dup")); err != nil || string(b) != "same" {
		t.Fatal("protected duplicate changed")
	}
}

func TestProtectedFileIsNotCatalogued(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "protected.bin")
	write(t, protected, "secret fixture", time.Hour)
	out := filepath.Join(t.TempDir(), "out")
	if code := runScan([]string{"-root", root, "-out", out, "-images=false", "-protect", protected}); code != 0 {
		t.Fatal("scan failed")
	}
	body, err := os.ReadFile(filepath.Join(out, "catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "protected.bin") {
		t.Fatal("protected file was read and catalogued")
	}
}

func TestDarwinProtectedRootsRefuseCaseAliases(t *testing.T) {
	if hostPlatform() != "darwin" {
		t.Skip("Darwin filesystem aliases")
	}
	home := t.TempDir()
	protected := filepath.Join(home, ".ssh")
	if err := os.Mkdir(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, ".SSH")
	a, err := os.Stat(protected)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(alias)
	if err != nil || !os.SameFile(a, b) {
		t.Skip("case-sensitive filesystem")
	}
	t.Setenv("HOME", home)
	policy, err := newStoragePolicy("workstation", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.excludes(filepath.Join(alias, "private-key")) {
		t.Fatal("case alias bypasses protected root")
	}
}

func TestDuplicateCleanupUsesReviewedRowsSnapshot(t *testing.T) {
	_, dup, plan, _ := applyFixture(t)
	groups, order, err := readDuplicatePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(t.TempDir(), "protected")
	write(t, protected, "same-content", 48*time.Hour)
	info, err := os.Stat(protected)
	if err != nil {
		t.Fatal(err)
	}
	for _, rows := range groups {
		for i := range rows {
			if rows[i].act == "dup" {
				rows[i].path = protected
				rows[i].mt = info.ModTime().Unix()
			}
		}
	}
	report, err := cleanupDuplicateRows(context.Background(), groups, order, true, 1, storagePolicy{Protected: []string{protected}})
	if err == nil || report.Changed != 0 {
		t.Fatalf("consumed protected rows accepted: %+v %v", report, err)
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatal("protected file removed")
	}
	// A later disk-plan edit cannot replace the already reviewed rows.
	groups, order, err = readDuplicatePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, []byte("invalid replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = cleanupDuplicateRows(context.Background(), groups, order, true, 1, storagePolicy{})
	if err != nil || report.Changed != 1 {
		t.Fatalf("snapshot was reread: %+v %v", report, err)
	}
	if _, err := os.Stat(dup); !os.IsNotExist(err) {
		t.Fatal("reviewed duplicate retained")
	}
}

func TestDarwinProtectedRootsRefuseUnicodeFoldAliases(t *testing.T) {
	if hostPlatform() != "darwin" {
		t.Skip("Darwin filesystem aliases")
	}
	root := t.TempDir()
	protected := filepath.Join(root, "σ")
	if err := os.Mkdir(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "ς")
	a, err := os.Stat(protected)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(alias)
	if err != nil || !os.SameFile(a, b) {
		t.Skip("filesystem does not fold this Unicode pair")
	}
	policy, err := newStoragePolicy("", []string{protected})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.excludes(filepath.Join(alias, "payload")) {
		t.Fatal("Unicode case-fold alias bypasses protection")
	}
}
