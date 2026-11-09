package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func cacheFixture(t *testing.T, suffix, file string) cachePlan {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, suffix)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(path, file)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, file), []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := cacheTargetAt(context.Background(), path)
	if err != nil || target == nil {
		t.Fatalf("target: %v, %v", target, err)
	}
	return cachePlan{SchemaVersion: 2, Platform: hostPlatform(), Root: root, Targets: []cacheTarget{*target}}
}

func TestLanguageCacheCanonicalCatalog(t *testing.T) {
	for _, suffix := range []string{".cache/go-build", "go/pkg/mod", ".cache/pip", ".cache/uv", ".npm/_cacache", ".local/share/pnpm/store", ".cache/yarn", ".yarn/berry/cache", ".cargo/registry/cache", ".gradle/caches", ".nuget/packages", ".local/share/NuGet/v3-cache", ".m2/repository"} {
		t.Run(suffix, func(t *testing.T) {
			p := cacheFixture(t, suffix, "fixture.crate")
			if p.Targets[0].Bytes != 6 || p.Targets[0].Files != 1 {
				t.Fatalf("incorrect measurement: %+v", p.Targets[0])
			}
			if err := validateCachePlan(p); err != nil {
				t.Fatal(err)
			}
			if (suffix == ".m2/repository" || suffix == ".cache/yarn" || suffix == ".yarn/berry/cache") && (p.Targets[0].Cleanup || p.Targets[0].Reason == "") {
				t.Fatal("protected or unsupported cache is actionable")
			}
		})
	}
}

func TestLanguageCacheRejectsForgedPlans(t *testing.T) {
	p := cacheFixture(t, ".cargo/registry/cache", "index/payload.crate")
	for name, modify := range map[string]func(*cachePlan){
		"outside":       func(p *cachePlan) { p.Root = filepath.Join(p.Root, "other") },
		"forged kind":   func(p *cachePlan) { p.Targets[0].Kind = "uv" },
		"relative":      func(p *cachePlan) { p.Targets[0].Path = "relative" },
		"duplicate":     func(p *cachePlan) { p.Targets = append(p.Targets, p.Targets[0]) },
		"identity":      func(p *cachePlan) { p.Targets[0].Inode = 0 },
		"negative size": func(p *cachePlan) { p.Targets[0].Bytes = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			q := p
			q.Targets = append([]cacheTarget{}, p.Targets...)
			modify(&q)
			if validateCachePlan(q) == nil {
				t.Fatal("forged plan accepted")
			}
		})
	}
	maven := cacheFixture(t, ".m2/repository", "local.jar")
	maven.Targets[0].Cleanup = true
	if validateCachePlan(maven) == nil {
		t.Fatal("forged Maven cleanup accepted")
	}
}

func TestLanguageCacheRejectsProtectedAndSymlinkPaths(t *testing.T) {
	root := t.TempDir()
	for _, suffix := range []string{".cargo", ".gradle", ".nuget", "node_modules/.cache/uv", ".git/.cache/go-build", ".venv/.cache/pip", ".yarn/cache"} {
		target, err := cacheTargetAt(context.Background(), filepath.Join(root, suffix))
		if err != nil || target != nil {
			t.Fatalf("protected %s: %v %v", suffix, target, err)
		}
	}
	p := cacheFixture(t, ".cargo/registry/cache", "index/payload.crate")
	if err := os.Symlink(p.Root, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheTargetAt(context.Background(), filepath.Join(root, "link/.cargo/registry/cache")); err == nil {
		t.Fatal("symlink component accepted")
	}
}

func TestLanguageCacheCustomEnvironmentAndProtectedRoots(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(root, "build-cache")
	if err := os.MkdirAll(filepath.Join(custom, "00"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCACHE", custom)
	target, err := cacheTargetAt(context.Background(), custom)
	if err != nil || target == nil || target.Kind != "go_build" {
		t.Fatalf("custom cache: %+v %v", target, err)
	}
	p := cachePlan{SchemaVersion: 2, Platform: hostPlatform(), Root: root, Targets: []cacheTarget{*target}}
	if err := os.Rename(filepath.Join(custom, "00"), filepath.Join(root, "old-marker")); err != nil {
		t.Fatal(err)
	}
	if err := validateCachePlan(p); err != nil {
		t.Fatalf("repeat plan depends on erased marker: %v", err)
	}
	for _, name := range []string{".cargo", ".gradle", ".nuget", ".m2", ".npm", ".yarn"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(path, "00"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GOCACHE", path)
		target, err := cacheTargetAt(context.Background(), path)
		if err != nil || target != nil {
			t.Fatalf("protected env root accepted: %s, %v", path, err)
		}
	}
}

func TestLanguageCacheOverlapsWithInterveningSibling(t *testing.T) {
	p := cacheFixture(t, ".cache/go-build", "README")
	parent := p.Targets[0]
	sibling := parent
	sibling.Kind = "uv"
	sibling.Path += "-other/.cache/uv"
	nested := parent
	nested.Kind = "pip"
	nested.Path += "/.cache/pip"
	p.Targets = []cacheTarget{parent, sibling, nested}
	if validateCachePlan(p) == nil {
		t.Fatal("overlap hidden by sorted sibling")
	}
}

func TestLanguageCacheLeavesBareRootForGitDiscovery(t *testing.T) {
	p := cacheFixture(t, ".gradle/caches", "payload.jar")
	cacheBareGitFixture(t, p.Targets[0].Path)
	target, err := cacheTargetAt(context.Background(), p.Targets[0].Path)
	if err != nil || target != nil {
		t.Fatalf("bare repository is classified as a cache: %+v %v", target, err)
	}
}

func cacheBareGitFixture(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "init", "--bare", "--template=", "--initial-branch=fixture", path)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init bare fixture: %v: %s", err, output)
	}
}
