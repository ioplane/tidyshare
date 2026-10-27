package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Losing command registration or cache/Git inventory must break actual CLI output.
func TestDeveloperStorageCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := tidyshareTestBinary(t)
	root, out := t.TempDir(), t.TempDir()
	cache := filepath.Join(root, "home", "dev", ".cache", "go-build")
	payload := filepath.Join(cache, "00", strings.Repeat("0", 64)+"-d")
	write(t, filepath.Join(cache, "README"), "This directory holds cached build artifacts from the Go build system.\n", time.Hour)
	write(t, payload, "generated-cache", time.Hour)
	localArtifact := filepath.Join(root, "home", "dev", ".m2", "repository", "local", "only.jar")
	write(t, localArtifact, "unpublished-local-package", time.Hour)
	gitRoot := filepath.Join(root, "source")
	for _, path := range []string{"objects", "refs/heads"} {
		if err := os.MkdirAll(filepath.Join(gitRoot, ".git", path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(gitRoot, ".git", "HEAD"), "ref: refs/heads/fixture\n", time.Hour)
	call := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, output)
		}
	}
	call("discover", "-root", root, "-out", out, "-formats", "json,jsonl,md")
	var inventory struct {
		Caches []struct {
			Path    string `json:"path"`
			Cleanup bool   `json:"cleanup"`
		} `json:"caches"`
		GitTrees []struct {
			Path string `json:"path"`
		} `json:"git_trees"`
	}
	body, err := os.ReadFile(filepath.Join(out, "discovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Caches) != 2 || len(inventory.GitTrees) != 1 || inventory.GitTrees[0].Path != gitRoot {
		t.Fatalf("wrong inventory: %s", body)
	}
	plan := filepath.Join(out, "cache-plan.json")
	for _, name := range []string{"discovery.jsonl", "discovery.md", "cache-plan.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatal(err)
		}
	}
	inside := exec.CommandContext(ctx, binary, "cleanup", "-kind", "caches", "-plan", plan, "-out", filepath.Join(cache, "reports"), "-yes")
	if output, err := inside.CombinedOutput(); err == nil {
		t.Fatalf("cleanup accepted reports inside its target: %s", output)
	}
	if _, err := os.Stat(payload); err != nil {
		t.Fatal("report preflight modified cache", err)
	}
	for _, step := range []struct {
		name    string
		execute bool
		changed int
	}{{"dry", false, 0}, {"execute", true, 1}, {"repeat", true, 0}} {
		output := filepath.Join(t.TempDir(), step.name)
		args := []string{"cleanup", "-kind", "caches", "-plan", plan, "-out", output, "-formats", "json,jsonl,md"}
		if step.execute {
			args = append(args, "-yes")
		}
		call(args...)
		body, err := os.ReadFile(filepath.Join(output, "cleanup.json"))
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Changed int `json:"changed"`
		}
		wantChanged := step.changed
		if runtime.GOOS == "darwin" {
			wantChanged = 0
		}
		if err := json.Unmarshal(body, &report); err != nil || report.Changed != wantChanged {
			t.Fatalf("%s changed=%d want=%d err=%v report=%s", step.name, report.Changed, step.changed, err, body)
		}
		if !step.execute {
			if _, err := os.Stat(payload); err != nil {
				t.Fatal("dry run lost payload", err)
			}
		}
	}
	if _, err := os.Stat(payload); runtime.GOOS == "linux" && !os.IsNotExist(err) {
		t.Fatalf("payload remains: %v", err)
	}
	if b, err := os.ReadFile(localArtifact); err != nil || string(b) != "unpublished-local-package" {
		t.Fatalf("protected local package changed: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(gitRoot, ".git", "HEAD")); err != nil {
		t.Fatal("Git source modified", err)
	}
}

func TestDiscoveryBareRepositoryAtCachePath(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, ".gradle", "caches")
	cacheBareGitFixture(t, bare)
	report, err := discoverStorage(context.Background(), root, t.TempDir())
	if err != nil || len(report.Warnings) != 0 || len(report.Caches) != 0 || len(report.GitTrees) != 1 {
		t.Fatalf("bare repository classified as cache: %+v %v", report, err)
	}
	if report.GitTrees[0].Path != bare || report.GitTrees[0].Kind != "bare" {
		t.Fatalf("wrong bare repository inventory: %+v", report.GitTrees)
	}
}

func TestDiscoveryRefusesReportsInsideCache(t *testing.T) {
	p := cacheFixture(t, ".cargo/registry/cache", "index/payload.crate")
	out := filepath.Join(p.Targets[0].Path, "review")
	if code := runDiscover([]string{"-root", p.Root, "-out", out}); code == 0 {
		t.Fatal("discovery published an unusable plan inside its cleanup target")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("discovery created output before location validation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.Targets[0].Path, "index/payload.crate")); err != nil {
		t.Fatal("discovery changed cache payload", err)
	}
}
