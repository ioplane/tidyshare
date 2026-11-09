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
	managedChromeRoot = "Library/Caches/Google/Chrome"
	managedXcodeRoot  = "Library/Developer/Xcode/DerivedData"
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
		if err := planManagedKind(ctx, &p, o, kind, &remaining, qualify); err != nil {
			return p, err
		}
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	return p, validateManagedPlan(p, storagePolicy{})
}

func planManagedKind(ctx context.Context, p *managedPlan, o managedPlanOptions, kind string, remaining *int, qualify func(context.Context, string) (*managedTool, error)) error {
	var err error
	var paths []string
	switch kind {
	case "go_build", "npm":
		paths = managedCachePaths(o.Root, kind)
	case "homebrew":
		paths = []string{filepath.Join(o.Root, "Library/Caches/Homebrew/downloads")}
	case "chrome", "xcode":
		var base string
		paths, base, err = managedApplicationPaths(o.Root, kind)
		if err != nil {
			p.Targets = append(p.Targets, managedTarget{Kind: kind, Path: base, State: "blocked", Reason: err.Error(), Entries: []managedEntry{}})
			return nil
		}
	case "docker_build":
		p.Targets = append(p.Targets, managedDockerTarget(ctx, o.Docker))
		return nil
	default:
		return fmt.Errorf("unsupported managed kind: %s", kind)
	}
	sort.Strings(paths)
	paths = uniqueStrings(paths)
	if len(paths) == 0 && (kind == "chrome" || kind == "xcode") {
		base := filepath.Join(o.Root, managedChromeRoot)
		if kind == "xcode" {
			base = filepath.Join(o.Root, managedXcodeRoot)
		}
		p.Targets = append(p.Targets, managedTarget{Kind: kind, Path: base, State: "absent", Reason: "no recognized cache subtrees", Entries: []managedEntry{}})
		return nil
	}
	for _, path := range paths {
		target, err := planManagedTarget(ctx, p, kind, path, remaining, qualify)
		p.Targets = append(p.Targets, target)
		if err != nil {
			return err
		}
	}
	return nil
}

func managedCachePaths(root, kind string) []string {
	var paths []string
	for _, layout := range cacheLayouts {
		if layout.kind == kind && (runtime.GOOS == "darwin" || !strings.HasPrefix(layout.suffix, "Library/")) {
			paths = append(paths, filepath.Join(root, layout.suffix))
		}
	}
	for _, key := range []string{"GOCACHE", "NPM_CONFIG_CACHE", "npm_config_cache"} {
		path := os.Getenv(key)
		if kind == "npm" {
			path = filepath.Join(path, "_cacache")
		}
		if filepath.IsAbs(path) && cacheWithin(root, path) && cacheKind(path, false) == kind {
			paths = append(paths, path)
		}
	}
	return paths
}

func managedApplicationPaths(root, kind string) ([]string, string, error) {
	if kind == "chrome" {
		base := filepath.Join(root, managedChromeRoot)
		paths, err := managedChildren(base, func(name string) []string {
			if !managedChromeProfile(name) {
				return nil
			}
			return []string{filepath.Join(name, "Cache"), filepath.Join(name, "Code Cache")}
		})
		return paths, base, err
	}
	base := filepath.Join(root, managedXcodeRoot)
	paths, err := managedChildren(base, func(name string) []string {
		return []string{filepath.Join(name, "Build/Intermediates.noindex"), filepath.Join(name, "Logs")}
	})
	return paths, base, err
}

func managedDockerTarget(ctx context.Context, docker *dockerStore) managedTarget {
	target := managedTarget{Kind: "docker_build", Path: "docker-build-cache", State: "blocked", Reason: "disposable daemon and builder qualification is required; no prune capability is enabled", Scope: "daemon default build cache older than 24h; without all; no image, container or volume deletion", Entries: []managedEntry{}}
	if docker != nil {
		if _, _, err := docker.inventory(ctx); err != nil {
			target.Reason += "; Docker observation unavailable: " + err.Error()
		} else {
			docker.mu.Lock()
			target.DaemonID = docker.DaemonID
			target.DockerContext = docker.Context
			docker.mu.Unlock()
		}
	}
	return target
}

func planManagedTarget(ctx context.Context, p *managedPlan, kind, path string, remaining *int, qualify func(context.Context, string) (*managedTool, error)) (managedTarget, error) {
	target := managedTarget{Kind: kind, Path: path, State: "absent", Scope: "regular files older than 24h from the reviewed manifest", Entries: []managedEntry{}}
	if p.Policy.overlaps(path) {
		target.State = "blocked"
		target.Reason = "protected by workstation policy"
		return target, nil
	}
	dir, err := openPlainDirectory(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return target, nil
	}
	if err != nil {
		target.State = "blocked"
		target.Reason = err.Error()
		return target, nil
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
		err = planManagedManifest(ctx, dir, st, p.CreatedAt, path, &target, remaining)
	}
	_ = dir.Close()
	if err != nil {
		target.State = "blocked"
		target.Reason = err.Error()
		target.Entries = []managedEntry{}
	}
	if managedUncertain(err) {
		return target, err
	}
	return target, nil
}

func planManagedManifest(ctx context.Context, dir *os.File, st unix.Stat_t, createdAt time.Time, path string, target *managedTarget, remaining *int) error {
	cutoff := createdAt.Add(-24 * time.Hour)
	_, _, err := cacheWalk(ctx, dir, uint64(st.Dev), func(parent *os.File, name string, s unix.Stat_t) error {
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
		if len(target.Entries) >= *remaining {
			return errors.New("manifest exceeds the 100000-entry plan bound")
		}
		target.Entries = append(target.Entries, managedEntry{Name: rel, Identity: managedStat(s)})
		return nil
	})
	if err == nil {
		target.State = "planned"
		*remaining -= len(target.Entries)
	}
	return err
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
		return managedChromeTargetAllowed(root, t)
	case "xcode":
		return managedXcodeTargetAllowed(root, t)
	}
	return false
}

func managedChromeTargetAllowed(root string, t managedTarget) bool {
	base := filepath.Join(root, managedChromeRoot)
	if t.Path == base {
		return (t.State == "blocked" || t.State == "absent") && len(t.Entries) == 0
	}
	rel, err := filepath.Rel(base, t.Path)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	return len(parts) == 2 && managedChromeProfile(parts[0]) && (parts[1] == "Cache" || parts[1] == "Code Cache")
}

func managedXcodeTargetAllowed(root string, t managedTarget) bool {
	base := filepath.Join(root, managedXcodeRoot)
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
		if err := validateManagedTarget(p, t, p.Targets[:i], policy, mandatory, &count); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedTarget(p managedPlan, t managedTarget, previous []managedTarget, policy, mandatory storagePolicy, count *int) error {
	if !managedTargetAllowed(p.Root, t) || (t.State != "planned" && t.State != "blocked" && t.State != "absent") || t.Entries == nil {
		return errors.New("invalid managed target kind, scope or state")
	}
	if t.State == "planned" && (policy.overlaps(t.Path) || mandatory.overlaps(t.Path) || t.Identity.Inode == 0 || t.Identity.UID != uint32(os.Getuid()) || t.Identity.Mode&unix.S_IFMT != unix.S_IFDIR) {
		return errors.New("managed target is protected or has invalid identity")
	}
	if err := validateManagedScope(t); err != nil {
		return err
	}
	for _, other := range previous {
		if cacheWithin(other.Path, t.Path) || cacheWithin(t.Path, other.Path) {
			return errors.New("managed targets overlap")
		}
	}
	seen := map[string]bool{}
	for _, e := range t.Entries {
		*count++
		if *count > managedEntryLimit || e.Name == "." || filepath.IsAbs(e.Name) || filepath.Clean(e.Name) != e.Name || e.Name == ".." || strings.HasPrefix(e.Name, ".."+string(os.PathSeparator)) || managedLockedName(e.Name) || managedProjectPath(e.Name) || seen[e.Name] || e.Identity.Device != t.Identity.Device || e.Identity.UID != t.Identity.UID || e.Identity.Inode == 0 || e.Identity.Links != 1 || e.Identity.Mode&unix.S_IFMT != unix.S_IFREG || e.Identity.Size < 0 || !time.Unix(e.Identity.Mtime.Sec, e.Identity.Mtime.Nsec).Before(p.CreatedAt.Add(-24*time.Hour)) {
			return errors.New("invalid managed manifest entry")
		}
		seen[e.Name] = true
	}
	return nil
}

func validateManagedScope(t managedTarget) error {
	if t.State != "planned" {
		return nil
	}
	if t.Kind == "go_build" || t.Kind == "npm" {
		if t.Tool == nil || t.Tool.Version == "" || len(t.Tool.SHA256) != 64 || len(t.Entries) != 0 || t.Scope != "native manager operation; not a file whitelist" {
			return errors.New("invalid native manager qualification or scope")
		}
	} else if t.Tool != nil || t.Scope != "regular files older than 24h from the reviewed manifest" {
		return errors.New("invalid manifest operation scope")
	}
	return nil
}
