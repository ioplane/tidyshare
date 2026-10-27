package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func inventoryFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	return root, filepath.Join(t.TempDir(), "report")
}

func readWorkstationFixture(t *testing.T, out string) workstationReport {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, "workstation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r workstationReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// A home-wide recursive scan or duplicate hardlink accounting breaks this test.
func TestWorkstationInventoryBoundedAccounting(t *testing.T) {
	root, out := inventoryFixture(t)
	cache := filepath.Join(root, ".npm/_cacache")
	write(t, filepath.Join(cache, "one"), "hello", time.Hour)
	if err := os.Link(filepath.Join(cache, "one"), filepath.Join(cache, "two")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "unselected", ".cache", "uv", "payload"), "do-not-discover", time.Hour)
	if code := runWorkstation([]string{"-root", root, "-out", out}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	r := readWorkstationFixture(t, out)
	found := false
	for _, c := range r.Categories {
		if strings.Contains(c.Path, "unselected") {
			t.Fatal("recursive home discovery")
		}
		if c.Path == cache {
			found = true
			if c.State != "complete" || c.LogicalBytes == nil || *c.LogicalBytes != 5 || c.AllocatedBytes == nil || c.Files != 2 {
				t.Fatalf("accounting: %+v", c)
			}
		}
	}
	if !found || r.SchemaVersion != 1 || r.Operation != "workstation" || len(r.Filesystems) != 1 {
		t.Fatalf("report: %+v", r)
	}
	for _, name := range []string{"workstation.json", "workstation.md"} {
		st, err := os.Stat(filepath.Join(out, name))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("private file %s: %v %v", name, st, err)
		}
	}
	st, err := os.Stat(out)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatal("report directory not private")
	}
}

// Removing the application-data policy must expose an environment-selected cache.
func TestWorkstationInventoryProtectsApplicationData(t *testing.T) {
	root, out := inventoryFixture(t)
	cache := filepath.Join(root, "Library/Application Support/Google/Chrome/profile")
	write(t, filepath.Join(cache, "00", "secret"), "profile", time.Hour)
	t.Setenv("GOCACHE", cache)
	if runWorkstation([]string{"-root", root, "-out", out}) != 0 {
		t.Fatal("inventory failed")
	}
	r := readWorkstationFixture(t, out)
	for _, c := range r.Categories {
		if c.Path == cache && (c.State != "protected" || c.LogicalBytes != nil) {
			t.Fatalf("protected cache read: %+v", c)
		}
	}
	p, err := newStoragePolicy("workstation", nil)
	if err != nil || !p.excludes(filepath.Join(root, "Pictures", "Photos Library.photoslibrary", "data")) {
		t.Fatal("Photos not protected")
	}
}

// A symlink must become a partial observation, never a zero-sized complete cache.
func TestWorkstationInventoryPartialAndOverlap(t *testing.T) {
	root, out := inventoryFixture(t)
	if err := os.MkdirAll(filepath.Join(root, ".npm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".npm/_cacache")); err != nil {
		t.Fatal(err)
	}
	if runWorkstation([]string{"-root", root, "-out", out}) == 0 {
		t.Fatal("partial inventory reported success")
	}
	r := readWorkstationFixture(t, out)
	if r.Complete {
		t.Fatal("partial report marked complete")
	}
	for _, c := range r.Categories {
		if c.Path == filepath.Join(root, ".npm/_cacache") && (c.State != "partial" || c.AllocatedBytes != nil) {
			t.Fatalf("symlink: %+v", c)
		}
	}
	if runWorkstation([]string{"-root", root, "-out", filepath.Join(root, ".npm/_cacache/out")}) == 0 {
		t.Fatal("unsafe output accepted")
	}
	if runWorkstation([]string{"-root", root, "-project-root", root, "-project-root", filepath.Join(root, ".npm"), "-out", filepath.Join(t.TempDir(), "out")}) == 0 {
		t.Fatal("overlapping project roots accepted")
	}
}

// Comparison must reject a different host and preserve a known same-category delta.
func TestWorkstationInventoryComparison(t *testing.T) {
	root, first := inventoryFixture(t)
	path := filepath.Join(root, ".npm/_cacache/payload")
	write(t, path, "first", time.Hour)
	if runWorkstation([]string{"-root", root, "-out", first}) != 0 {
		t.Fatal("first scan")
	}
	write(t, path, "first plus", time.Hour)
	second := filepath.Join(t.TempDir(), "second")
	if runWorkstation([]string{"-root", root, "-out", second, "-previous", filepath.Join(first, "workstation.json")}) != 0 {
		t.Fatal("second scan")
	}
	r := readWorkstationFixture(t, second)
	if !r.Comparison.Compatible {
		t.Fatalf("comparison: %+v", r.Comparison)
	}
	found := false
	for _, c := range r.Categories {
		if c.Path == filepath.Dir(path) {
			found = c.LogicalDelta != nil && *c.LogicalDelta == 5
		}
	}
	if !found {
		t.Fatal("missing +5 logical delta")
	}
	prior := readWorkstationFixture(t, first)
	prior.Host = "another-host"
	b, _ := json.Marshal(prior)
	changed := filepath.Join(t.TempDir(), "prior.json")
	if err := os.WriteFile(changed, b, 0o600); err != nil {
		t.Fatal(err)
	}
	third := filepath.Join(t.TempDir(), "third")
	if runWorkstation([]string{"-root", root, "-out", third, "-previous", changed}) != 0 {
		t.Fatal("incompatible previous must not fail current scan")
	}
	r = readWorkstationFixture(t, third)
	if r.Comparison.Compatible || r.Comparison.Reason == "" {
		t.Fatal("foreign report accepted")
	}
	for _, c := range r.Categories {
		if c.LogicalDelta != nil {
			t.Fatal("foreign delta emitted")
		}
	}
}

func TestWorkstationInventoryCancellation(t *testing.T) {
	root, _ := inventoryFixture(t)
	write(t, filepath.Join(root, ".npm/_cacache/data"), "fixture", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := collectWorkstation(ctx, workstationOptions{Root: root})
	if err == nil || r.Complete || len(r.Warnings) == 0 {
		t.Fatalf("cancellation lost: %+v %v", r, err)
	}
}

func TestWorkstationPreviousRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "previous")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readWorkstationPrevious(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(300 * time.Millisecond):
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		select {
		case err := <-done:
			t.Logf("released blocked FIFO reader: %v", err)
		case <-time.After(time.Second):
			t.Fatal("blocked reader could not be released")
		}
		t.Fatal("previous report blocked on FIFO")
	}
}

func TestWorkstationValidationFailureReportIsPartial(t *testing.T) {
	root, out := inventoryFixture(t)
	if runWorkstation([]string{"-root", filepath.Join(root, "missing"), "-out", out}) == 0 {
		t.Fatal("missing root succeeded")
	}
	r := readWorkstationFixture(t, out)
	if r.Complete || len(r.Warnings) == 0 {
		t.Fatalf("failed validation looks complete: %+v", r)
	}
}

func TestWorkstationLinuxProfileOverridesRemainProtected(t *testing.T) {
	root, out := inventoryFixture(t)
	cache := filepath.Join(root, ".config/google-chrome/Default")
	write(t, filepath.Join(cache, "Login Data"), "sensitive", time.Hour)
	t.Setenv("GOCACHE", cache)
	if runWorkstation([]string{"-root", root, "-out", out}) != 0 {
		t.Fatal("inventory failed")
	}
	r := readWorkstationFixture(t, out)
	found := false
	for _, c := range r.Categories {
		if c.Path == cache {
			found = c.State == "protected" && c.LogicalBytes == nil
		}
	}
	if !found {
		t.Fatal("Linux browser profile measured as cache")
	}
}

func TestWorkstationRejectsNonAdjacentProjectOverlap(t *testing.T) {
	root, out := inventoryFixture(t)
	args := []string{"-root", root, "-out", out}
	for _, suffix := range []string{"a", "a-other", "a/sub"} {
		path := filepath.Join(root, suffix)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-project-root", path)
	}
	if runWorkstation(args) == 0 {
		t.Fatal("non-adjacent overlapping roots accepted")
	}
}

func TestWorkstationPodmanAggregateIsExplicitlyUnsupported(t *testing.T) {
	source := workstationRuntimeSource{Engine: "podman"}
	r, err := source.observe(context.Background())
	if err == nil || r.State != "unsupported" || !strings.Contains(r.Reason, "unsupported") {
		t.Fatalf("unqualified Podman aggregate: %+v %v", r, err)
	}
}

func TestWorkstationMarkdownIncludesScopeProjectsAndDeltas(t *testing.T) {
	_, out := inventoryFixture(t)
	v := int64(7)
	r := workstationReport{Host: "fixture-host", Root: "/fixture/root", Platform: "fixture-platform", Policy: storagePolicy{Profile: "workstation", Protected: []string{"/fixture/keep"}}, GitTrees: []gitTree{{Path: "/fixture/root/repo", Kind: "worktree", Branch: "fixture-branch"}}, Categories: []workstationCategory{{Kind: "fixture", Path: "/fixture/cache", State: "complete", Files: 3, LogicalDelta: &v}}, Filesystems: []workstationFilesystem{{Device: 1, AvailableDelta: &v}}}
	if err := writeWorkstationReports(out, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "workstation.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"fixture-host", "/fixture/root", "/fixture/keep", "/fixture/root/repo", "fixture-branch", "Logical delta", "Available delta", "Files", "7"} {
		if !strings.Contains(string(b), value) {
			t.Errorf("Markdown omits %q", value)
		}
	}
}

func TestWorkstationGitTreesAcrossExplicitRootsAreUnique(t *testing.T) {
	root, _ := inventoryFixture(t)
	main := gitFixture(t, root, "main")
	linked := filepath.Join(root, "linked")
	fixtureGit(t, main, "worktree", "add", "--detach", linked)
	r, err := collectWorkstation(context.Background(), workstationOptions{Root: root, ProjectRoots: []string{main, linked}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tree := range r.GitTrees {
		if seen[tree.Path] {
			t.Fatal("duplicate registered worktree")
		}
		seen[tree.Path] = true
	}
	if len(seen) != 2 {
		t.Fatalf("trees %+v", r.GitTrees)
	}
}

func TestWorkstationProposesManagedPlanningWithoutCleanupAuthorization(t *testing.T) {
	root, _ := inventoryFixture(t)
	for _, suffix := range []string{".cache/go-build/fixture", ".npm/_cacache/fixture", ".cache/uv/fixture"} {
		write(t, filepath.Join(root, suffix), "fixture", time.Hour)
	}
	r, err := collectWorkstation(context.Background(), workstationOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Categories {
		switch c.Kind {
		case "go_build", "npm", "homebrew", "chrome", "xcode":
			if c.Operation != "managed_plan" {
				t.Fatalf("managed planning route absent: %+v", c)
			}
		case "uv":
			if c.Operation != "inventory_only" {
				t.Fatal("uv execution route proposed")
			}
		}
	}
}
