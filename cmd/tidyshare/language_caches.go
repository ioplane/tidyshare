package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type cacheTarget struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Files   int64  `json:"files"`
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	UID     uint32 `json:"uid"`
	Cleanup bool   `json:"cleanup"`
	Reason  string `json:"reason,omitempty"`
}

type cachePlan struct {
	SchemaVersion int           `json:"schema_version"`
	Root          string        `json:"root"`
	Platform      string        `json:"platform,omitempty"`
	Policy        storagePolicy `json:"policy,omitempty"`
	Targets       []cacheTarget `json:"targets"`
}

var cacheLayouts = []struct{ suffix, kind string }{
	{".cache/go-build", "go_build"},
	{"Library/Caches/go-build", "go_build"},
	{"Library/Caches/pip", "pip"},
	{"Library/Caches/Homebrew", "homebrew"},
	{"Library/Developer/Xcode/DerivedData", "xcode"},
	{"Library/Caches/pnpm", "pnpm"},
	{"go/pkg/mod", "go_modules"},
	{".cache/pip", "pip"},
	{".cache/uv", "uv"},
	{".npm/_cacache", "npm"},
	{".local/share/pnpm/store", "pnpm"},
	{".cache/yarn", "yarn"},
	{".yarn/berry/cache", "yarn_berry"},
	{".cargo/registry/cache", "cargo"},
	{".gradle/caches", "gradle"},
	{".nuget/packages", "nuget_packages"},
	{".local/share/NuGet/v3-cache", "nuget_http"},
	{".m2/repository", "maven"},
}

func cacheWithin(root, path string) bool {
	return path == root || (root == "/" && filepath.IsAbs(path)) || strings.HasPrefix(path, root+string(os.PathSeparator))
}

func cacheKind(path string, discovery bool) string {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return ""
	}
	switch filepath.Base(path) {
	case ".cargo", ".gradle", ".nuget", ".m2", ".npm", ".yarn", ".cache", ".local", "node_modules", ".venv", "venv":
		return ""
	}
	for c := range strings.SplitSeq(path, "/") {
		switch c {
		case ".git", ".venv", "venv", "node_modules", ".rustup":
			return ""
		}
	}
	for _, entry := range cacheLayouts {
		if strings.HasSuffix(path, "/"+entry.suffix) {
			return entry.kind
		}
	}
	// Only current absolute environment settings qualify custom roots; plans cannot invent them.
	settings := []struct{ env, suffix, kind string }{
		{"GOCACHE", "", "go_build"},
		{"GOMODCACHE", "", "go_modules"},
		{"PIP_CACHE_DIR", "", "pip"},
		{"UV_CACHE_DIR", "", "uv"},
		{"NPM_CONFIG_CACHE", "_cacache", "npm"},
		{"npm_config_cache", "_cacache", "npm"},
		{"CARGO_HOME", "registry/cache", "cargo"},
		{"GRADLE_USER_HOME", "caches", "gradle"},
		{"NUGET_PACKAGES", "", "nuget_packages"},
		{"NUGET_HTTP_CACHE_PATH", "", "nuget_http"},
		{"XDG_CACHE_HOME", "go-build", "go_build"},
		{"XDG_CACHE_HOME", "pip", "pip"},
		{"XDG_CACHE_HOME", "uv", "uv"},
		{"XDG_CACHE_HOME", "yarn", "yarn"},
		{"XDG_DATA_HOME", "pnpm/store", "pnpm"},
		{"XDG_DATA_HOME", "NuGet/v3-cache", "nuget_http"},
	}
	home, _ := os.UserHomeDir()
	for _, setting := range settings {
		base := os.Getenv(setting.env)
		if !filepath.IsAbs(base) || filepath.Clean(base) != base || base == "/" || base == home {
			continue
		}
		if path == filepath.Join(base, setting.suffix) && (!discovery || cacheCustomMarker(path, setting.kind)) {
			return setting.kind
		}
	}
	for _, base := range filepath.SplitList(os.Getenv("GOPATH")) {
		if filepath.IsAbs(base) && path == filepath.Join(base, "pkg/mod") {
			return "go_modules"
		}
	}
	return ""
}

func cacheCustomMarker(path, kind string) bool {
	markers := map[string][]string{
		"go_build": {"00"}, "go_modules": {"cache/download"}, "pip": {"http-v2", "wheels"},
		"uv": {"archive-v0", "wheels-v5", "wheels-v6", "sdists-v9", "git-v0"}, "npm": {"content-v2", "index-v5"}, "cargo": {},
		"gradle": {"CACHEDIR.TAG", "modules-2"}, "nuget_packages": {}, "nuget_http": {},
		"pnpm": {"v3", "v10"}, "yarn": {"v6"},
	}
	for _, marker := range markers[kind] {
		if st, err := os.Lstat(filepath.Join(path, marker)); err == nil && st.Mode()&os.ModeSymlink == 0 {
			return true
		}
	}
	if kind == "cargo" || kind == "nuget_packages" || kind == "nuget_http" {
		dir, err := openPlainDirectory(path, false)
		if err != nil {
			return false
		}
		defer dir.Close()
		return cachePackageMarker(dir, kind, 0)
	}
	return false
}

func cachePackageMarker(dir *os.File, kind string, depth int) bool {
	entries, _ := dir.ReadDir(128)
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			if (kind == "cargo" && strings.HasSuffix(entry.Name(), ".crate")) || (kind == "nuget_packages" && entry.Name() == ".nupkg.metadata") || (kind == "nuget_http" && strings.HasSuffix(entry.Name(), ".dat")) {
				return true
			}
		}
		if entry.IsDir() && depth < 2 {
			fd, err := unix.Openat(int(dir.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				continue
			}
			child := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), entry.Name()))
			found := cachePackageMarker(child, kind, depth+1)
			_ = child.Close()
			if found {
				return true
			}
		}
	}
	return false
}

func cacheTargetAt(ctx context.Context, path string) (*cacheTarget, error) {
	kind := cacheKind(path, true)
	if kind == "" {
		return nil, nil
	}
	f, err := openPlainDirectory(path, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	bare, err := cacheBareRepository(f)
	if err != nil {
		return nil, err
	}
	if bare {
		return nil, nil
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nil, err
	}
	bytes, files, err := cacheWalk(ctx, f, uint64(st.Dev), nil)
	if err != nil {
		return nil, err
	}
	target := &cacheTarget{Kind: kind, Path: path, Bytes: bytes, Files: files, Device: uint64(st.Dev), Inode: st.Ino, UID: st.Uid, Cleanup: cacheCleanupSupported(kind)}
	if kind == "yarn" || kind == "yarn_berry" {
		target.Reason = "unsupported native cleanup: rc/plugins and executable selection require qualification"
	}
	if kind == "maven" {
		target.Reason = "protected local repository may contain unpublished packages"
	}
	if !target.Cleanup && target.Reason == "" {
		target.Reason = "native cleanup is not qualified for this cache on this platform"
	}
	return target, nil
}

func cacheCleanupSupported(kind string) bool {
	if runtime.GOOS == "darwin" {
		switch kind {
		case "go_build", "go_modules", "pip", "uv", "npm", "pnpm":
			return true
		default:
			return false
		}
	}
	return kind != "homebrew" && kind != "xcode" && kind != "maven" && kind != "yarn" && kind != "yarn_berry"
}

func validateCachePlan(p cachePlan) error {
	if (p.SchemaVersion != 1 && p.SchemaVersion != 2) || !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || p.Targets == nil {
		return errors.New("invalid cache plan root or schema")
	}
	if p.SchemaVersion == 1 && runtime.GOOS != "linux" {
		return errors.New("legacy cache plans require Linux; run discover again")
	}
	if p.SchemaVersion == 2 && p.Platform != runtime.GOOS {
		return errors.New("cache plan platform differs; run discover again")
	}
	policy, err := newStoragePolicy(p.Policy.Profile, p.Policy.Protected)
	if err != nil {
		return err
	}
	paths := make([]string, len(p.Targets))
	for i, t := range p.Targets {
		if policy.overlaps(t.Path) || !cacheWithin(p.Root, t.Path) || cacheKind(t.Path, false) == "" || cacheKind(t.Path, false) != t.Kind || t.Inode == 0 || t.Bytes < 0 || t.Files < 0 || t.Cleanup != cacheCleanupSupported(t.Kind) {
			return errors.New("invalid cache target qualification or identity")
		}
		paths[i] = t.Path
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i]+"/" < paths[j]+"/" })
	for i := 1; i < len(paths); i++ {
		if cacheWithin(paths[i-1], paths[i]) {
			return errors.New("cache targets overlap")
		}
	}

	return nil
}

// cacheWalk pins each directory, does not follow links, and keeps only one directory batch per depth.
func cacheWalk(ctx context.Context, dir *os.File, device uint64, visit func(*os.File, string, unix.Stat_t) error) (int64, int64, error) {
	return cacheWalkDepth(ctx, dir, device, visit, 0)
}

func cacheWalkDepth(ctx context.Context, dir *os.File, device uint64, visit func(*os.File, string, unix.Stat_t) error, depth int) (int64, int64, error) {
	if depth > 128 {
		return 0, 0, errors.New("cache nesting exceeds safety bound")
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if visit != nil {
		var st unix.Stat_t
		if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
			return 0, 0, err
		}
		if err := visit(dir, ".", st); err != nil {
			return 0, 0, err
		}
	}
	if _, err := dir.Seek(0, 0); err != nil {
		return 0, 0, err
	}
	var bytes, files int64
	for {
		if err := ctx.Err(); err != nil {
			return bytes, files, err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return bytes, files, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return bytes, files, err
			}
			var st unix.Stat_t
			if err := unix.Fstatat(int(dir.Fd()), entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return bytes, files, err
			}
			if uint64(st.Dev) != device {
				return bytes, files, errors.New("cache crosses a filesystem boundary")
			}
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				fd, err := unix.Openat(int(dir.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return bytes, files, err
				}
				child := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), entry.Name()))
				var opened unix.Stat_t
				err = unix.Fstat(fd, &opened)
				if err == nil && (opened.Dev != st.Dev || opened.Ino != st.Ino) {
					err = errors.New("cache directory changed during traversal")
				}
				if err == nil && visit != nil {
					err = visit(dir, entry.Name(), st)
				}
				var childBytes, childFiles int64
				if err == nil {
					childBytes, childFiles, err = cacheWalkDepth(ctx, child, device, visit, depth+1)
				}
				_ = child.Close()
				bytes += childBytes
				files += childFiles
				if err != nil {
					return bytes, files, err
				}
			case unix.S_IFREG:
				if st.Size > 0 && bytes > (1<<63-1)-st.Size {
					return bytes, files, errors.New("cache size overflows report")
				}
				bytes += st.Size
				files++
				if visit != nil {
					if err := visit(dir, entry.Name(), st); err != nil {
						return bytes, files, err
					}
				}
			case unix.S_IFLNK:
				if visit != nil {
					if err := visit(dir, entry.Name(), st); err != nil {
						return bytes, files, err
					}
				}
			default:
				if visit != nil {
					return bytes, files, fmt.Errorf("cache contains a special file: %s", entry.Name())
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return bytes, files, nil
		}
	}
}
