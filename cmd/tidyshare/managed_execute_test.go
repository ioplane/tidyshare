package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagedEntryChangeKeepsUnderlyingCause(t *testing.T) {
	cause := &os.PathError{Op: "open", Path: "owned-fixture", Err: syscall.ENOENT}
	err := managedEntryOpenError(cause)
	var pathError *os.PathError
	if !errors.Is(err, errManagedEntryChanged) || !errors.Is(err, syscall.ENOENT) || !errors.As(err, &pathError) || pathError != cause {
		t.Fatalf("underlying cause lost: %v", err)
	}
}

func managedExecutionFixture(t *testing.T) (managedPlan, string) {
	t.Helper()
	root, _ := inventoryFixture(t)
	cache := filepath.Join(root, "Library/Caches/Homebrew/downloads")
	write(t, filepath.Join(cache, "one"), "one", 48*time.Hour)
	write(t, filepath.Join(cache, "two"), "two", 48*time.Hour)
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"homebrew"}})
	if err != nil {
		t.Fatal(err)
	}
	return p, cache
}

func TestManagedExecutionRequiresDigestAndTargets(t *testing.T) {
	p, cache := managedExecutionFixture(t)
	digest := strings.Repeat("a", 64)
	for _, options := range []managedExecuteOptions{{Execute: true, Targets: []string{cache}}, {Execute: true, ExpectedDigest: digest}, {Execute: true, ExpectedDigest: strings.Repeat("b", 64), Targets: []string{cache}}, {Execute: true, ExpectedDigest: digest, Targets: []string{filepath.Dir(cache)}}} {
		if _, err := executeManagedPlan(context.Background(), p, digest, options); err == nil {
			t.Fatal("execution gate bypassed")
		}
	}
	if _, err := os.Stat(filepath.Join(cache, "one")); err != nil {
		t.Fatal("unapproved operation changed data")
	}
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{})
	if err != nil || !r.DryRun || r.Changed != 0 {
		t.Fatalf("dry run %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(cache, "one")); err != nil {
		t.Fatal("dry run removed data")
	}
}

// Replacing a reviewed inode, adding a file, or editing content must preserve that file.
func TestManagedExecutionPreservesChangedAndNewFiles(t *testing.T) {
	p, cache := managedExecutionFixture(t)
	write(t, filepath.Join(cache, "one"), "changed", 48*time.Hour)
	write(t, filepath.Join(cache, "new"), "new", 48*time.Hour)
	digest := strings.Repeat("a", 64)
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: managedFixtureJournal(t)})
	if err != nil || r.Changed != 1 {
		t.Fatalf("result %+v %v", r, err)
	}
	for _, name := range []string{"one", "new"} {
		if _, err := os.Stat(filepath.Join(cache, name)); err != nil {
			t.Fatalf("changed/new file removed: %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(cache, "two")); !os.IsNotExist(err) {
		t.Fatal("reviewed unchanged file not removed")
	}
}

func TestManagedExecutionCanaryAndSymlinkReplacement(t *testing.T) {
	p, cache := managedExecutionFixture(t)
	digest := strings.Repeat("a", 64)
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: managedFixtureJournal(t), Limit: 1})
	if err != nil || r.Changed != 1 {
		t.Fatalf("canary %+v %v", r, err)
	}
	p, cache = managedExecutionFixture(t)
	outside := filepath.Join(t.TempDir(), "keep")
	write(t, outside, "keep", 48*time.Hour)
	if err := os.Remove(filepath.Join(cache, "one")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cache, "one")); err != nil {
		t.Fatal(err)
	}
	r, err = executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: managedFixtureJournal(t)})
	if err != nil || r.Changed != 1 {
		t.Fatalf("symlink replacement %+v %v", r, err)
	}
	b, err := os.ReadFile(outside)
	if err != nil || string(b) != "keep" {
		t.Fatal("outside file changed")
	}
}

func TestManagedNativeQualificationAndConfigurationIsolation(t *testing.T) {
	for _, kind := range []string{"go_build", "npm"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := inventoryFixture(t)
			bad := filepath.Join(root, "bad.js")
			marker := filepath.Join(root, "injected")
			if err := os.WriteFile(bad, []byte("require('fs').writeFileSync("+strconvQuoteForFixture(marker)+",'bad')"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NODE_OPTIONS", "--require="+bad)
			name := "go"
			if kind == "npm" {
				name = "npm"
			}
			if _, err := exec.LookPath(name); err != nil {
				t.Skipf("manager unavailable: %v", err)
			}
			tool, err := qualifyManagedTool(context.Background(), kind)
			if err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(root, "cache")
			seed := filepath.Join(cache, "00/fixture-a")
			if kind == "npm" {
				cache = filepath.Join(root, "npm/_cacache")
				seed = filepath.Join(cache, "content-v2/sha512/00/fixture")
			}
			write(t, seed, "owned", 48*time.Hour)
			result, err := runManagedNative(context.Background(), managedTarget{Kind: kind, Path: cache}, tool)
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("native result %+v %v", result, err)
			}
			if _, err := os.Stat(seed); !os.IsNotExist(err) {
				t.Fatal("native fixture was not cleared")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("ambient NODE_OPTIONS executed")
			}
			tool.SHA256 = strings.Repeat("0", 64)
			if _, err := runManagedNative(context.Background(), managedTarget{Kind: kind, Path: cache}, tool); err == nil {
				t.Fatal("modified executable digest accepted")
			}
		})
	}
}

func strconvQuoteForFixture(s string) string { return "'" + strings.ReplaceAll(s, "'", "\\'") + "'" }

func TestManagedCLIPlanAndDryRun(t *testing.T) {
	root, out := inventoryFixture(t)
	write(t, filepath.Join(root, "Library/Caches/Homebrew/downloads/old"), "cache", 48*time.Hour)
	if runManagedPlan([]string{"-root", root, "-kind", "homebrew", "-out", out}) != 0 {
		t.Fatal("plan CLI failed")
	}
	p, digest, err := readManagedPlan(filepath.Join(out, "managed-plan.json"))
	if err != nil || p.Contract != "managed" || len(digest) != 64 {
		t.Fatalf("plan read %s %v", digest, err)
	}
	if runManagedClean([]string{"-plan", filepath.Join(out, "managed-plan.json"), "-out", filepath.Join(t.TempDir(), "dry")}) != 0 {
		t.Fatal("dry-run CLI failed")
	}
	if _, err := os.Stat(filepath.Join(root, "Library/Caches/Homebrew/downloads/old")); err != nil {
		t.Fatal("CLI dry run changed data")
	}
}

func managedFixtureJournal(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "journal.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestManagedJournalFailureStopsBeforeDeletion(t *testing.T) {
	p, cache := managedExecutionFixture(t)
	journal := managedFixtureJournal(t)
	journal.Close()
	digest := strings.Repeat("a", 64)
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: journal})
	if err == nil || r.Changed != 0 {
		t.Fatalf("failed journal did not stop cleanup: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(cache, "one")); err != nil {
		t.Fatal("cleanup ran without durable intent")
	}
}

func TestManagedLsofProcessAndDescriptorRecords(t *testing.T) {
	for _, item := range []struct {
		output    string
		wantError bool
	}{
		{"p123\nf4\n", false},
		{"p999\nf4\n", true},
		{"f4\n", true},
		{"pbad\n", true},
		{"p123\nnunexpected\n", true},
	} {
		if err := managedLsofProcesses(item.output, 123); (err != nil) != item.wantError {
			t.Fatalf("output %q: %v", item.output, err)
		}
	}
}

func TestManagedNativeExecutionBlocksNewRepository(t *testing.T) {
	root, _ := inventoryFixture(t)
	cache := filepath.Join(root, ".cache/go-build")
	write(t, filepath.Join(cache, "00/source"), "preserve", 48*time.Hour)
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"go_build"}})
	if err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, cache, "init", "-b", "fixture")
	digest := strings.Repeat("a", 64)
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: managedFixtureJournal(t)})
	if err == nil || r.Changed != 0 {
		t.Fatalf("new repository did not block native cleanup: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(cache, "00/source")); err != nil {
		t.Fatal("new repository data removed")
	}
}

func managedReplacementTool(t *testing.T, name, marker string) string {
	t.Helper()
	bin := t.TempDir()
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf marker > "+quoted+"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestManagedChangedManagerNeverRunsDuringRequalification(t *testing.T) {
	root, _ := inventoryFixture(t)
	cache := filepath.Join(root, ".cache/go-build")
	write(t, filepath.Join(cache, "00/fixture-a"), "keep", 48*time.Hour)
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"go_build"}})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "unreviewed-manager-ran")
	bin := managedReplacementTool(t, "go", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	digest := strings.Repeat("a", 64)
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: managedFixtureJournal(t)})
	if err == nil || r.Changed != 0 {
		t.Fatalf("changed manager not blocked: %+v %v", r, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("unreviewed manager executed before fingerprint verification")
	}
}

func TestManagedObserverDoesNotExecutePATHReplacement(t *testing.T) {
	p, cache := managedExecutionFixture(t)
	marker := filepath.Join(p.Root, "unreviewed-observer-ran")
	bin := managedReplacementTool(t, "lsof", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	digest := strings.Repeat("a", 64)
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: managedFixtureJournal(t)})
	if err != nil || r.Changed != 2 {
		t.Fatalf("system observer failed: %+v %v", r, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("unreviewed PATH observer executed")
	}
}

func TestManagedPermissionFailureIsPartialError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses fixture directory permissions")
	}
	p, cache := managedExecutionFixture(t)
	protected := filepath.Join(cache, "readonly")
	write(t, filepath.Join(protected, "old"), "keep", 48*time.Hour)
	var err error
	p, err = createManagedPlan(context.Background(), managedPlanOptions{Root: p.Root, Kinds: []string{"homebrew"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(protected, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(protected, 0o700) })
	digest := strings.Repeat("a", 64)
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: managedFixtureJournal(t)})
	if err == nil || r.Error == "" || r.Changed == 0 {
		t.Fatalf("permission error reported success: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(protected, "old")); err != nil {
		t.Fatal("readonly file changed")
	}
}

func TestManagedManifestExecutionBlocksNewRepository(t *testing.T) {
	p, cache := managedExecutionFixture(t)
	fixtureGit(t, cache, "init", "-b", "fixture")
	digest := strings.Repeat("a", 64)
	r, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: managedFixtureJournal(t)})
	if err == nil || r.Changed != 0 {
		t.Fatalf("new repository did not block manifest cleanup: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(cache, "one")); err != nil {
		t.Fatal("repository data changed")
	}
}

func TestManagedIntentRecordsClaimRecoveryPath(t *testing.T) {
	p, cache := managedExecutionFixture(t)
	journal := managedFixtureJournal(t)
	digest := strings.Repeat("a", 64)
	if _, err := executeManagedPlan(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: []string{cache}, Journal: journal}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(journal.Name())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var item managedResult
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatal(err)
		}
		if item.Status == "intent" && item.Name != "" {
			count++
			if filepath.Dir(filepath.Dir(item.RecoveryPath)) != cache || filepath.Base(item.RecoveryPath) != "entry" {
				t.Fatalf("claim recovery path missing from intent: %+v", item)
			}
		}
	}
	if count != 2 {
		t.Fatal("missing entry intents")
	}
}

func TestManagedMarkdownPrintsFilesystemValuesAndCommandStatus(t *testing.T) {
	before, after := uint64(4096), uint64(8192)
	r := managedExecutionReport{FilesystemBefore: []workstationFilesystem{{Device: 1, Path: "/fixture", AvailableBytes: &before}}, FilesystemAfter: []workstationFilesystem{{Device: 1, Path: "/fixture", AvailableBytes: &after}}, Results: []managedResult{{Target: "/fixture/cache", Status: "native_completed", Command: &managedCommandResult{ExitCode: 0}}}}
	var output bytes.Buffer
	if err := writeManagedCleanupMarkdown(&output, r); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"4096", "8192", "exit code: 0"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("Markdown omits %s: %s", value, output.String())
		}
	}
	if strings.Contains(output.String(), "0x") {
		t.Fatal("Markdown prints pointer addresses instead of measurements")
	}
}
