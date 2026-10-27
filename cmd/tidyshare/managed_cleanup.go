package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	managedEntryLimit = 100000
	managedPlanLimit  = 64 << 20
)

type managedIdentity struct {
	Device uint64        `json:"device"`
	Inode  uint64        `json:"inode"`
	UID    uint32        `json:"uid"`
	Mode   uint32        `json:"mode"`
	Links  uint64        `json:"links"`
	Size   int64         `json:"size"`
	Mtime  unix.Timespec `json:"mtime"`
	Ctime  unix.Timespec `json:"ctime"`
}

type managedEntry struct {
	Name     string          `json:"name"`
	Identity managedIdentity `json:"identity"`
}

type managedTarget struct {
	Kind          string          `json:"kind"`
	Path          string          `json:"path"`
	State         string          `json:"state"`
	Reason        string          `json:"reason,omitempty"`
	Scope         string          `json:"scope"`
	Identity      managedIdentity `json:"identity"`
	Entries       []managedEntry  `json:"entries"`
	Skipped       int             `json:"skipped"`
	Tool          *managedTool    `json:"tool,omitempty"`
	DaemonID      string          `json:"daemon_id,omitempty"`
	DockerContext string          `json:"docker_context,omitempty"`
}

type managedPlan struct {
	SchemaVersion int             `json:"schema_version"`
	Contract      string          `json:"contract"`
	Platform      string          `json:"platform"`
	Root          string          `json:"root"`
	RootIdentity  managedIdentity `json:"root_identity"`
	Policy        storagePolicy   `json:"policy"`
	CreatedAt     time.Time       `json:"created_at"`
	Targets       []managedTarget `json:"targets"`
}

type managedPlanOptions struct {
	Root   string
	Kinds  []string
	Policy storagePolicy
	Docker *dockerStore
}

func managedStat(st unix.Stat_t) managedIdentity {
	return managedIdentity{uint64(st.Dev), st.Ino, st.Uid, uint32(st.Mode), uint64(st.Nlink), st.Size, st.Mtim, st.Ctim}
}

func managedPolicy(root string, p storagePolicy) (storagePolicy, error) {
	p.Protected = append([]string{}, p.Protected...)
	if p.Profile != "" && p.Profile != "workstation" {
		return p, errors.New("managed cleanup requires the workstation policy")
	}
	for _, suffix := range workstationProtectedSuffixes {
		p.Protected = append(p.Protected, filepath.Join(root, suffix))
	}
	p, err := newStoragePolicy("workstation", p.Protected)
	if err == nil {
		p.Protected = uniqueStrings(p.Protected)
	}
	return p, err
}

func createManagedPlan(ctx context.Context, o managedPlanOptions) (managedPlan, error) {
	return createManagedPlanWithQualifier(ctx, o, qualifyManagedTool)
}

func createManagedPlanWithQualifier(ctx context.Context, o managedPlanOptions, qualify func(context.Context, string) (*managedTool, error)) (managedPlan, error) {
	p := managedPlan{SchemaVersion: 1, Contract: "managed", Platform: runtime.GOOS, Root: o.Root, CreatedAt: time.Now().UTC(), Targets: []managedTarget{}}
	if !filepath.IsAbs(o.Root) || filepath.Clean(o.Root) != o.Root || o.Root == "/" {
		return p, errors.New("managed root must be a clean absolute user directory")
	}
	root, err := openPlainDirectory(o.Root, false)
	if err != nil {
		return p, err
	}
	defer root.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &st); err != nil {
		return p, err
	}
	p.RootIdentity = managedStat(st)
	if st.Uid != uint32(os.Getuid()) {
		return p, errors.New("managed root belongs to another user")
	}
	p.Policy, err = managedPolicy(o.Root, o.Policy)
	if err != nil {
		return p, err
	}
	kinds := append([]string{}, o.Kinds...)
	if len(kinds) == 0 {
		kinds = []string{"go_build", "npm", "homebrew", "chrome", "xcode", "docker_build"}
	}
	sort.Strings(kinds)
	kinds = uniqueStrings(kinds)
	remaining := managedEntryLimit
	for _, kind := range kinds {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		var paths []string
		switch kind {
		case "go_build", "npm":
			for _, layout := range cacheLayouts {
				if layout.kind == kind && (runtime.GOOS == "darwin" || !strings.HasPrefix(layout.suffix, "Library/")) {
					paths = append(paths, filepath.Join(o.Root, layout.suffix))
				}
			}
			for _, key := range []string{"GOCACHE", "NPM_CONFIG_CACHE", "npm_config_cache"} {
				path := os.Getenv(key)
				if kind == "npm" {
					path = filepath.Join(path, "_cacache")
				}
				if filepath.IsAbs(path) && cacheWithin(o.Root, path) && cacheKind(path, false) == kind {
					paths = append(paths, path)
				}
			}
		case "homebrew":
			paths = []string{filepath.Join(o.Root, "Library/Caches/Homebrew/downloads")}
		case "chrome":
			base := filepath.Join(o.Root, "Library/Caches/Google/Chrome")
			paths, err = managedChildren(base, func(name string) []string {
				if !managedChromeProfile(name) {
					return nil
				}
				return []string{filepath.Join(name, "Cache"), filepath.Join(name, "Code Cache")}
			})
			if err != nil {
				p.Targets = append(p.Targets, managedTarget{Kind: kind, Path: base, State: "blocked", Reason: err.Error(), Entries: []managedEntry{}})
				continue
			}
		case "xcode":
			base := filepath.Join(o.Root, "Library/Developer/Xcode/DerivedData")
			paths, err = managedChildren(base, func(name string) []string {
				return []string{filepath.Join(name, "Build/Intermediates.noindex"), filepath.Join(name, "Logs")}
			})
			if err != nil {
				p.Targets = append(p.Targets, managedTarget{Kind: kind, Path: base, State: "blocked", Reason: err.Error(), Entries: []managedEntry{}})
				continue
			}
		case "docker_build":
			target := managedTarget{Kind: kind, Path: "docker-build-cache", State: "blocked", Reason: "disposable daemon and builder qualification is required; no prune capability is enabled", Scope: "daemon default build cache older than 24h; without all; no image, container or volume deletion", Entries: []managedEntry{}}
			if o.Docker != nil {
				if _, _, err := o.Docker.inventory(ctx); err != nil {
					target.Reason += "; Docker observation unavailable: " + err.Error()
				} else {
					o.Docker.mu.Lock()
					target.DaemonID = o.Docker.DaemonID
					target.DockerContext = o.Docker.Context
					o.Docker.mu.Unlock()
				}
			}
			p.Targets = append(p.Targets, target)
			continue
		default:
			return p, fmt.Errorf("unsupported managed kind: %s", kind)
		}
		sort.Strings(paths)
		paths = uniqueStrings(paths)
		if len(paths) == 0 && (kind == "chrome" || kind == "xcode") {
			base := filepath.Join(o.Root, "Library/Caches/Google/Chrome")
			if kind == "xcode" {
				base = filepath.Join(o.Root, "Library/Developer/Xcode/DerivedData")
			}
			p.Targets = append(p.Targets, managedTarget{Kind: kind, Path: base, State: "absent", Reason: "no recognized cache subtrees", Entries: []managedEntry{}})
			continue
		}
		for _, path := range paths {
			target := managedTarget{Kind: kind, Path: path, State: "absent", Scope: "regular files older than 24h from the reviewed manifest", Entries: []managedEntry{}}
			if p.Policy.overlaps(path) {
				target.State = "blocked"
				target.Reason = "protected by workstation policy"
				p.Targets = append(p.Targets, target)
				continue
			}
			dir, err := openPlainDirectory(path, false)
			if errors.Is(err, os.ErrNotExist) {
				p.Targets = append(p.Targets, target)
				continue
			}
			if err != nil {
				target.State = "blocked"
				target.Reason = err.Error()
				p.Targets = append(p.Targets, target)
				continue
			}
			var st unix.Stat_t
			err = unix.Fstat(int(dir.Fd()), &st)
			target.Identity = managedStat(st)
			if err == nil && st.Uid != uint32(os.Getuid()) {
				err = errors.New("target belongs to another user")
			}
			if err == nil && (kind == "go_build" || kind == "npm") {
				target.Scope = "native manager operation; not a file whitelist"
				_, _, err = cacheWalk(ctx, dir, uint64(st.Dev), cacheGuardEntry(kind, path))
				if err == nil {
					target.Tool, err = qualify(ctx, kind)
				}
				if err == nil {
					target.State = "planned"
				}
			} else if err == nil {
				cutoff := p.CreatedAt.Add(-24 * time.Hour)
				_, _, err = cacheWalk(ctx, dir, uint64(st.Dev), func(parent *os.File, name string, s unix.Stat_t) error {
					if err := managedProjectGuard(parent, name, s); err != nil {
						return err
					}
					if s.Mode&unix.S_IFMT != unix.S_IFREG {
						return nil
					}
					rel, e := filepath.Rel(path, filepath.Join(parent.Name(), name))
					if e != nil {
						return e
					}
					if managedLockedName(rel) || s.Uid != uint32(os.Getuid()) || s.Nlink != 1 || !time.Unix(s.Mtim.Sec, s.Mtim.Nsec).Before(cutoff) {
						target.Skipped++
						return nil
					}
					if len(target.Entries) >= remaining {
						return errors.New("manifest exceeds the 100000-entry plan bound")
					}
					target.Entries = append(target.Entries, managedEntry{Name: rel, Identity: managedStat(s)})
					return nil
				})
				if err == nil {
					target.State = "planned"
					remaining -= len(target.Entries)
				}
			}
			_ = dir.Close()
			if err != nil {
				target.State = "blocked"
				target.Reason = err.Error()
				target.Entries = []managedEntry{}
			}
			p.Targets = append(p.Targets, target)
			if managedUncertain(err) {
				return p, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	return p, validateManagedPlan(p, storagePolicy{})
}

func managedChildren(base string, children func(string) []string) ([]string, error) {
	dir, err := openPlainDirectory(base, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(4096)
	if len(entries) == 4096 {
		return nil, errors.New("application root exceeds the 4095-entry discovery bound")
	}
	if err != nil && len(entries) == 0 && err.Error() != "EOF" {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			for _, rel := range children(entry.Name()) {
				paths = append(paths, filepath.Join(base, rel))
			}
		}
	}
	return paths, nil
}

func managedChromeProfile(name string) bool {
	if name == "Default" {
		return true
	}
	if !strings.HasPrefix(name, "Profile ") {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, "Profile "))
	return err == nil && n >= 0
}

func managedLockedName(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		lower := strings.ToLower(part)
		if lower == "locks" || lower == "lock" || strings.HasSuffix(lower, ".lock") || strings.HasPrefix(lower, ".tidyshare-") {
			return true
		}
	}
	return false
}

func managedProjectPath(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

func managedProjectGuard(parent *os.File, name string, st unix.Stat_t) error {
	if managedProjectPath(name) {
		return errors.New("cache contains a Git project or worktree; preserve the category")
	}
	if name == "." && st.Mode&unix.S_IFMT == unix.S_IFDIR {
		bare, err := cacheBareRepository(parent)
		if err != nil {
			return err
		}
		if bare {
			return errors.New("cache contains a bare repository; preserve the category")
		}
	}
	return nil
}

func managedTargetAllowed(root string, t managedTarget) bool {
	if t.Kind == "docker_build" {
		return t.Path == "docker-build-cache" && t.State == "blocked" && len(t.Entries) == 0
	}
	if !filepath.IsAbs(t.Path) || filepath.Clean(t.Path) != t.Path || !cacheWithin(root, t.Path) {
		return false
	}
	switch t.Kind {
	case "go_build", "npm":
		return cacheKind(t.Path, false) == t.Kind
	case "homebrew":
		return t.Path == filepath.Join(root, "Library/Caches/Homebrew/downloads")
	case "chrome":
		base := filepath.Join(root, "Library/Caches/Google/Chrome")
		if t.Path == base {
			return (t.State == "blocked" || t.State == "absent") && len(t.Entries) == 0
		}
		rel, err := filepath.Rel(base, t.Path)
		if err != nil {
			return false
		}
		parts := strings.Split(rel, string(os.PathSeparator))
		return len(parts) == 2 && managedChromeProfile(parts[0]) && (parts[1] == "Cache" || parts[1] == "Code Cache")
	case "xcode":
		base := filepath.Join(root, "Library/Developer/Xcode/DerivedData")
		if t.Path == base {
			return (t.State == "blocked" || t.State == "absent") && len(t.Entries) == 0
		}
		rel, err := filepath.Rel(base, t.Path)
		if err != nil {
			return false
		}
		parts := strings.Split(rel, string(os.PathSeparator))
		return len(parts) >= 2 && parts[0] != ".." && (len(parts) == 2 && parts[1] == "Logs" || len(parts) == 3 && parts[1] == "Build" && parts[2] == "Intermediates.noindex")
	}
	return false
}

func validateManagedPlan(p managedPlan, mandatory storagePolicy) error {
	if p.SchemaVersion != 1 || p.Contract != "managed" || p.Platform != runtime.GOOS || !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || p.Root == "/" || p.RootIdentity.Inode == 0 || p.RootIdentity.UID != uint32(os.Getuid()) || p.Targets == nil || p.CreatedAt.IsZero() || p.CreatedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("invalid managed contract, platform, root or identity")
	}
	policy, err := managedPolicy(p.Root, p.Policy)
	if err != nil {
		return err
	}
	mandatory, err = managedPolicy(p.Root, mandatory)
	if err != nil {
		return err
	}
	count := 0
	for i, t := range p.Targets {
		if !managedTargetAllowed(p.Root, t) || (t.State != "planned" && t.State != "blocked" && t.State != "absent") || t.Entries == nil {
			return errors.New("invalid managed target kind, scope or state")
		}
		if t.State == "planned" && (policy.overlaps(t.Path) || mandatory.overlaps(t.Path) || t.Identity.Inode == 0 || t.Identity.UID != uint32(os.Getuid()) || t.Identity.Mode&unix.S_IFMT != unix.S_IFDIR) {
			return errors.New("managed target is protected or has invalid identity")
		}
		if t.State == "planned" {
			if t.Kind == "go_build" || t.Kind == "npm" {
				if t.Tool == nil || t.Tool.Version == "" || len(t.Tool.SHA256) != 64 || len(t.Entries) != 0 || t.Scope != "native manager operation; not a file whitelist" {
					return errors.New("invalid native manager qualification or scope")
				}
			} else if t.Tool != nil || t.Scope != "regular files older than 24h from the reviewed manifest" {
				return errors.New("invalid manifest operation scope")
			}
		}
		for _, other := range p.Targets[:i] {
			if cacheWithin(other.Path, t.Path) || cacheWithin(t.Path, other.Path) {
				return errors.New("managed targets overlap")
			}
		}
		seen := map[string]bool{}
		for _, e := range t.Entries {
			count++
			if count > managedEntryLimit || e.Name == "." || filepath.IsAbs(e.Name) || filepath.Clean(e.Name) != e.Name || e.Name == ".." || strings.HasPrefix(e.Name, ".."+string(os.PathSeparator)) || managedLockedName(e.Name) || managedProjectPath(e.Name) || seen[e.Name] || e.Identity.Device != t.Identity.Device || e.Identity.UID != t.Identity.UID || e.Identity.Inode == 0 || e.Identity.Links != 1 || e.Identity.Mode&unix.S_IFMT != unix.S_IFREG || e.Identity.Size < 0 || !time.Unix(e.Identity.Mtime.Sec, e.Identity.Mtime.Nsec).Before(p.CreatedAt.Add(-24*time.Hour)) {
				return errors.New("invalid managed manifest entry")
			}
			seen[e.Name] = true
		}
	}
	return nil
}
