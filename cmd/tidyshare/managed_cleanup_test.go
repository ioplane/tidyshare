package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Broadening a manifest to locks, fresh files, links, profiles or products breaks these fixtures.
func TestManagedManifestBoundaries(t *testing.T) {
	root, _ := inventoryFixture(t)
	for _, kind := range []string{"homebrew", "chrome", "xcode"} {
		t.Run(kind, func(t *testing.T) {
			var cache string
			switch kind {
			case "homebrew":
				cache = filepath.Join(root, "Library/Caches/Homebrew/downloads")
			case "chrome":
				cache = filepath.Join(root, "Library/Caches/Google/Chrome/Default/Cache")
			case "xcode":
				cache = filepath.Join(root, "Library/Developer/Xcode/DerivedData/fixture/Build/Intermediates.noindex")
			}
			write(t, filepath.Join(cache, "old"), "old", 48*time.Hour)
			write(t, filepath.Join(cache, "fresh"), "fresh", time.Minute)
			write(t, filepath.Join(cache, "cache.lock"), "lock", 48*time.Hour)
			write(t, filepath.Join(cache, "locks", "held"), "lock", 48*time.Hour)
			write(t, filepath.Join(cache, "linked"), "link", 48*time.Hour)
			if err := os.Link(filepath.Join(cache, "linked"), filepath.Join(cache, "other-link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(cache, "old"), filepath.Join(cache, "alias")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, "Library/Application Support/Google/Chrome/Default/Login Data"), "profile", 48*time.Hour)
			write(t, filepath.Join(root, "Library/Developer/Xcode/DerivedData/fixture/Build/Products/keep.app"), "product", 48*time.Hour)
			p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{kind}})
			if err != nil {
				t.Fatal(err)
			}
			if p.Contract != "managed" || p.SchemaVersion != 1 {
				t.Fatalf("contract %+v", p)
			}
			found := false
			for _, target := range p.Targets {
				if target.Path == cache {
					found = true
					if target.State != "planned" || len(target.Entries) != 1 || target.Entries[0].Name != "old" || target.Entries[0].Identity.Inode == 0 {
						t.Fatalf("manifest %+v", target)
					}
				}
			}
			if !found {
				t.Fatal("missing manifest target")
			}
			if err := validateManagedPlan(p, storagePolicy{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedPlanRejectsLegacyScopeAndOverlap(t *testing.T) {
	root, _ := inventoryFixture(t)
	cache := filepath.Join(root, "Library/Caches/Homebrew/downloads")
	write(t, filepath.Join(cache, "old"), "cache", 48*time.Hour)
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"homebrew"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*managedPlan){
		func(p *managedPlan) { p.Contract = "" },
		func(p *managedPlan) { p.Platform = "other" },
		func(p *managedPlan) {
			p.Targets[0].Path = filepath.Join(root, "Library/Application Support/Google/Chrome")
		},
		func(p *managedPlan) { p.Targets[0].Entries[0].Name = "../escape" },
		func(p *managedPlan) { p.Targets = append(p.Targets, p.Targets[0]) },
		func(p *managedPlan) { p.Targets[0].Scope = "no deletion" },
	} {
		q := p
		q.Targets = append([]managedTarget{}, p.Targets...)
		q.Targets[0].Entries = append([]managedEntry{}, p.Targets[0].Entries...)
		change(&q)
		if err := validateManagedPlan(q, storagePolicy{}); err == nil {
			t.Fatalf("forged scope accepted: %+v", q)
		}
	}
}

func TestManagedNativePlanPreservesRepositoryInsideCache(t *testing.T) {
	root, _ := inventoryFixture(t)
	cache := gitFixture(t, root, ".cache/go-build")
	write(t, filepath.Join(cache, "00/source.go"), "source", 48*time.Hour)
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"go_build"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, target := range p.Targets {
		if target.Path == cache {
			found = target.State == "blocked" && target.Reason != ""
		}
	}
	if !found {
		t.Fatal("source repository qualified for native clearing")
	}
}

func TestManagedManifestPlanPreservesRepositories(t *testing.T) {
	root, _ := inventoryFixture(t)
	cache := filepath.Join(root, "Library/Caches/Homebrew/downloads")
	gitFixture(t, root, "Library/Caches/Homebrew/downloads/source")
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"homebrew"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 1 || p.Targets[0].Path != cache || p.Targets[0].State != "blocked" {
		t.Fatalf("repository inside manifest cache admitted: %+v", p.Targets)
	}
}

func TestManagedDockerAndUVRemainBlocked(t *testing.T) {
	root, _ := inventoryFixture(t)
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"docker_build"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 1 || p.Targets[0].State != "blocked" || p.Targets[0].Reason == "" {
		t.Fatalf("Docker qualification %+v", p)
	}
	if _, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"uv"}}); err == nil {
		t.Fatal("uv execution adapter admitted")
	}
}

func TestManagedEmptyRootReportsAllDefaultCategories(t *testing.T) {
	root, _ := inventoryFixture(t)
	p, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, target := range p.Targets {
		found[target.Kind] = true
	}
	for _, kind := range []string{"go_build", "npm", "homebrew", "chrome", "xcode", "docker_build"} {
		if !found[kind] {
			t.Fatalf("missing category state: %s", kind)
		}
	}
}

func TestManagedPolicyConcurrentRootsRemainIndependent(t *testing.T) {
	base, _ := inventoryFixture(t)
	shared := storagePolicy{Profile: "workstation", Protected: make([]string, 1, 128)}
	shared.Protected[0] = filepath.Join(base, "extra")
	var group sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		group.Add(1)
		go func(name string) {
			defer group.Done()
			root := filepath.Join(base, name)
			other := "first"
			if name == "first" {
				other = "second"
			}
			for i := 0; i < 100; i++ {
				policy, err := managedPolicy(root, shared)
				if err != nil {
					t.Error(err)
					return
				}
				if !policy.excludes(filepath.Join(root, ".config/keep")) {
					t.Error("own protected roots lost")
					return
				}
				for _, path := range policy.Protected {
					if cacheWithin(filepath.Join(base, other), path) {
						t.Error("another request changed this root policy")
						return
					}
				}
			}
		}(name)
	}
	group.Wait()
}
