package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var errCacheUnsupported = errors.New("cache cleanup unsupported")

func cleanupCaches(ctx context.Context, p cachePlan, execute bool, limit int) (cleanupReport, error) {
	report := cleanupReport{SchemaVersion: 1, Operation: "cleanup_caches", DryRun: !execute, Results: []cleanupResult{}}
	if err := validateCachePlan(p); err != nil {
		return report, err
	}
	if limit < 0 {
		return report, errors.New("cache cleanup limit must be non-negative")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for i, target := range p.Targets {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		result := cleanupResult{Target: target.Path, Status: "skipped"}
		switch {
		case limit > 0 && i >= limit:
			result.Reason = "limit"
		case !target.Cleanup:
			result.Reason = target.Reason
		default:
			var err error
			result, err = cleanupCache(ctx, target, execute)
			report.Results = append(report.Results, result)
			if result.Status == "cleared" {
				report.Changed++
				report.LogicalBytes += result.Bytes
			}
			if result.Status == "skipped" {
				report.Skipped++
			}
			if err != nil {
				return report, err
			}
			continue
		}
		report.Results = append(report.Results, result)
		report.Skipped++
	}
	return report, nil
}

func cacheIdentity(f *os.File, target cacheTarget) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Dev != target.Device || st.Ino != target.Inode || st.Uid != target.UID || (os.Geteuid() != 0 && st.Uid != uint32(os.Geteuid())) {
		return errors.New("cache identity or owner changed")
	}
	return nil
}

func cleanupCache(ctx context.Context, target cacheTarget, execute bool) (cleanupResult, error) {
	result := cleanupResult{Target: target.Path, Status: "skipped"}
	dir, err := openPlainDirectory(target.Path, false)
	if errors.Is(err, os.ErrNotExist) {
		result.Status = "absent"
		return result, nil
	}
	if err != nil {
		result.Reason = "cache path unavailable or contains symlinks"
		return result, nil
	}
	defer dir.Close()
	if err = cacheIdentity(dir, target); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	// Re-open the visible path immediately before native execution; native tools use path names.
	guard := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := openPlainDirectory(target.Path, false)
		if err != nil {
			return err
		}
		defer current.Close()
		if err := cacheIdentity(current, target); err != nil {
			return err
		}
		if err := cacheMountGuard(target.Path); err != nil {
			return err
		}
		return cacheBusy(ctx, target.Path, []uintptr{dir.Fd(), current.Fd()})
	}
	if err = guard(); err != nil {
		result.Reason = err.Error()
		return result, ctx.Err()
	}
	before, beforeFiles, err := cacheWalk(ctx, dir, target.Device, cacheGuardEntry(target.Kind, target.Path))
	if err != nil {
		result.Reason = err.Error()
		return result, ctx.Err()
	}
	if beforeFiles == 0 && target.Kind != "uv" {
		result.Status = "already_empty"
		return result, nil
	}
	if !execute {
		result.Status = "would_clear"
		result.Bytes = before
		return result, nil
	}
	if err := guard(); err != nil {
		result.Reason = err.Error()
		return result, ctx.Err()
	}
	if target.Kind == "cargo" || target.Kind == "gradle" {
		err = clearCacheDirectory(ctx, dir, target.Device, target.Kind, 0)
	} else {
		err = nativeCacheClean(ctx, target)
	}
	if ctx.Err() != nil {
		result.Status = "interrupted"
		result.Reason = "cache cleanup interrupted; partial changes and totals are unknown"
		return result, ctx.Err()
	}
	// Even a failed native command may have cleared part of a cache: account observed changes.
	after, afterFiles, measureErr := cacheMeasureAfter(ctx, target)
	if measureErr != nil {
		result.Status = "interrupted"
		result.Reason = "cache cleanup completed without a complete post-measurement; partial changes and totals are unknown"
		return result, fmt.Errorf("measure cache after cleanup: %w", measureErr)
	}
	if after < before || afterFiles < beforeFiles {
		result.Status = "cleared"
		result.Bytes = max(int64(0), before-after)
	} else {
		result.Status = "skipped"
		result.Reason = "cache manager produced no measured net decrease"
	}
	if err != nil {
		result.Reason = err.Error()
		if result.Status != "cleared" {
			result.Status = "skipped"
		}
	}
	if err != nil && !errors.Is(err, errCacheUnsupported) {
		return result, err
	}
	return result, nil
}

func cacheMeasureAfter(ctx context.Context, target cacheTarget) (int64, int64, error) {
	dir, err := openPlainDirectory(target.Path, false)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	defer dir.Close()
	if err := cacheIdentity(dir, target); err != nil {
		return 0, 0, err
	}
	return cacheWalk(ctx, dir, target.Device, nil)
}

func cacheGuardEntry(kind, root string) func(*os.File, string, unix.Stat_t) error {
	return func(dir *os.File, name string, st unix.Stat_t) error {
		if name == "." {
			bare, err := cacheBareRepository(dir)
			if err != nil {
				return err
			}
			if bare && !cacheManagedBareRepository(kind, root, dir.Name()) {
				return errors.New("cache contains a bare Git repository outside managed native VCS paths")
			}
			return nil
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			if name == ".git" || name == "go.mod" || name == "pyproject.toml" || name == "Cargo.toml" || name == "package.json" {
				return errors.New("cache contains linked source metadata")
			}
			if kind == "uv" {
				return nil
			}
			return errors.New("cache contains a symlink")
		}
		if st.Mode&unix.S_IFMT == unix.S_IFREG && st.Nlink != 1 && (kind == "cargo" || kind == "gradle") {
			return errors.New("direct cache payload has multiple hard links")
		}

		if kind == "cargo" {
			if st.Mode&unix.S_IFMT == unix.S_IFREG && !strings.HasSuffix(name, ".crate") {
				return errors.New("cargo archive cache contains a non-archive file")
			}
			if name == ".git" {
				return errors.New("cache contains Git metadata")
			}
			return nil
		}
		// Native uv and Go module caches own extracted sources and cached VCS repositories.
		nativeSources := (kind == "go_modules" || kind == "nuget_packages") && dir.Name() != root
		if kind == "uv" && dir.Name() != root {
			relative, err := filepath.Rel(root, dir.Name())
			if err == nil {
				bucket := strings.SplitN(relative, string(os.PathSeparator), 2)[0]
				nativeSources = (bucket == "git-v0" || bucket == "git-v1") || strings.HasPrefix(bucket, "archive-v") || strings.HasPrefix(bucket, "sdists-v") || strings.HasPrefix(bucket, "wheels-v")
			}
		}
		if nativeSources {
			return nil
		}
		switch name {
		case ".git", ".hg", ".svn", "go.mod", "Cargo.toml", "package.json", "pyproject.toml", "settings.gradle", "settings.gradle.kts", "build.gradle", "build.gradle.kts", "gradle.properties", "init.gradle", "init.gradle.kts", "config.toml", "credentials", "credentials.toml", "NuGet.Config", "nuget.config", ".npmrc", ".yarnrc", ".yarnrc.yml":
			return errors.New("cache contains source metadata or configuration")
		}
		if st.Mode&unix.S_IFMT == unix.S_IFREG {
			switch filepath.Ext(name) {
			case ".go", ".rs", ".py", ".java", ".kt", ".groovy", ".c", ".h", ".cpp", ".js", ".ts":
				return errors.New("cache contains source files")
			}
		}
		return nil
	}
}

// cacheBareRepository checks the repository signature without reading config or following links.
func cacheBareRepository(dir *os.File) (bool, error) {
	for _, entry := range []struct {
		name string
		mode uint32
	}{{"HEAD", unix.S_IFREG}, {"objects", unix.S_IFDIR}, {"refs", unix.S_IFDIR}} {
		var st unix.Stat_t
		err := unix.Fstatat(int(dir.Fd()), entry.name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("inspect bare Git signature: %w", err)
		}
		if st.Mode&unix.S_IFMT != entry.mode {
			return false, nil
		}
	}
	return true, nil
}

func cacheManagedBareRepository(kind, root, path string) bool {
	if (kind != "go_modules" && kind != "uv") || root == "" || root == path {
		return false
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	parts := strings.Split(relative, string(os.PathSeparator))
	if len(parts) != 3 {
		return false
	}
	digest := parts[2]
	if strings.Trim(digest, "0123456789abcdef") != "" {
		return false
	}
	if kind == "go_modules" {
		return parts[0] == "cache" && parts[1] == "vcs" && len(digest) == 64
	}
	return (parts[0] == "git-v0" || parts[0] == "git-v1") && parts[1] == "db" && len(digest) >= 16 && len(digest) <= 64
}

// clearCacheDirectory removes only guarded children through pinned directory descriptors.
func clearCacheDirectory(ctx context.Context, dir *os.File, device uint64, kind string, depth int) error {
	if depth > 128 {
		return errors.New("cache nesting exceeds safety bound")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cacheGuardEntry(kind, "")(dir, ".", unix.Stat_t{Mode: unix.S_IFDIR}); err != nil {
		return err
	}
	if _, err := dir.Seek(0, 0); err != nil {
		return err
	}
	for {
		if _, err := dir.Seek(0, 0); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			var st unix.Stat_t
			if err := unix.Fstatat(int(dir.Fd()), entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			if st.Dev != device {
				return errors.New("cache crossed a filesystem boundary")
			}
			if err := cacheGuardEntry(kind, "")(dir, entry.Name(), st); err != nil {
				return err
			}
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				fd, err := unix.Openat(int(dir.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return err
				}
				child := os.NewFile(uintptr(fd), entry.Name())
				var opened unix.Stat_t
				err = unix.Fstat(fd, &opened)
				if err == nil && (opened.Ino != st.Ino || opened.Dev != st.Dev) {
					err = errors.New("cache directory replaced")
				}
				if err == nil {
					err = clearCacheDirectory(ctx, child, device, kind, depth+1)
				}
				_ = child.Close()
				if err != nil {
					return err
				}
				if err := unix.Unlinkat(int(dir.Fd()), entry.Name(), unix.AT_REMOVEDIR); err != nil {
					return err
				}
			case unix.S_IFREG:
				if st.Nlink != 1 {
					return errors.New("cache payload has multiple hard links")
				}
				if err := unix.Unlinkat(int(dir.Fd()), entry.Name(), 0); err != nil {
					return err
				}
			default:
				return errors.New("cache contains a symlink or special file")
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func cacheMountGuard(path string) error {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return errors.New("cannot inspect cache mount boundaries")
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			return errors.New("invalid mount inventory")
		}
		point := cacheEscapedProcPath(fields[4])
		if point != path && cacheWithin(path, point) {
			return errors.New("cache contains a nested mount")
		}
	}
	return scanner.Err()
}

func cacheEscapedProcPath(path string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(path)
}

func cacheLinkWithin(root, path string) bool {
	// readlink returns literal names; only proc text formats escape octal sequences.
	return cacheWithin(root, path) || cacheWithin(root, strings.TrimSuffix(path, " (deleted)"))
}

func cacheBusy(ctx context.Context, path string, ownFDs []uintptr) error {
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return errors.New("cannot inspect cache users")
	}
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := strconv.Atoi(process.Name()); err != nil {
			continue
		}
		base := filepath.Join("/proc", process.Name())
		checkLink := func(name string) error {
			link, err := os.Readlink(name)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return errors.New("cannot inspect a potentially relevant process")
			}
			if cacheLinkWithin(path, link) {
				return errors.New("cache is in use by a process")
			}
			return nil
		}
		for _, name := range []string{"cwd", "exe", "root"} {
			if err := checkLink(filepath.Join(base, name)); err != nil {
				return err
			}
		}
		descriptors, err := os.ReadDir(filepath.Join(base, "fd"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("cannot inspect process file descriptors")
		}
		for _, fd := range descriptors {
			ignore := false
			if process.Name() == strconv.Itoa(os.Getpid()) {
				for _, own := range ownFDs {
					if fd.Name() == strconv.FormatUint(uint64(own), 10) {
						ignore = true
						break
					}
				}
			}
			if !ignore {
				if err := checkLink(filepath.Join(base, "fd", fd.Name())); err != nil {
					return err
				}
			}
		}
		maps, err := os.Open(filepath.Join(base, "maps"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("cannot inspect process memory maps")
		}
		scanner := bufio.NewScanner(maps)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		busy := false
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				_ = maps.Close()
				return err
			}
			line := scanner.Text()
			if start := strings.Index(line, "/"); start >= 0 {
				mapped := line[start:]
				if cacheLinkWithin(path, mapped) || cacheLinkWithin(path, cacheEscapedProcPath(mapped)) {
					busy = true
					break
				}
			}
		}
		scanErr := scanner.Err()
		_ = maps.Close()
		if scanErr != nil {
			return errors.New("cannot inspect complete process memory maps")
		}
		if busy {
			return errors.New("cache has a memory-mapped payload")
		}
	}
	return nil
}

func nativeCacheClean(ctx context.Context, target cacheTarget) error {
	var executable string
	var args, extra []string
	switch target.Kind {
	case "go_build":
		executable = "go"
		args = []string{"clean", "-cache"}
		extra = []string{"GOCACHE=" + target.Path}
	case "go_modules":
		executable = "go"
		args = []string{"clean", "-modcache"}
		extra = []string{"GOMODCACHE=" + target.Path}
	case "pip":
		executable = "pip"
		args = []string{"--isolated", "--disable-pip-version-check", "--cache-dir", target.Path, "cache", "purge"}
		extra = []string{"PIP_CONFIG_FILE=/dev/null"}
	case "uv":
		executable = "uv"
		args = []string{"--no-config", "--cache-dir", target.Path, "cache", "clean", "--force"}
		extra = []string{"UV_LOCK_TIMEOUT=1", "UV_NO_PROGRESS=1"}
	case "npm":
		executable = "npm"
		args = []string{"cache", "clean", "--force", "--global", "--cache", filepath.Dir(target.Path), "--ignore-scripts", "--audit=false", "--fund=false"}
	case "pnpm":
		executable = "pnpm"
		args = []string{"store", "prune", "--store-dir", target.Path, "--ignore-scripts", "--config.ignore-pnpmfile=true", "--config.ignore-workspace=true"}
	case "yarn", "yarn_berry":
		return fmt.Errorf("%w: Yarn project plugins and executable selection require independent qualification", errCacheUnsupported)
	case "nuget_packages", "nuget_http":
		executable = "dotnet"
		local := "global-packages"
		env := "NUGET_PACKAGES="
		if target.Kind == "nuget_http" {
			local = "http-cache"
			env = "NUGET_HTTP_CACHE_PATH="
		}
		args = []string{"nuget", "locals", local, "--clear", "--force-english-output"}
		extra = []string{env + target.Path, "DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1"}
	default:
		return errors.New("unsupported cache manager")
	}
	commandPath, err := exec.LookPath(executable)
	if err != nil {
		return fmt.Errorf("%w: cache manager unavailable: %s", errCacheUnsupported, executable)
	}
	scratch, err := os.MkdirTemp("", "tidyshare-cache-command-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if target.Kind == "npm" || target.Kind == "pnpm" {
		userConfig := filepath.Join(scratch, "user.npmrc")
		globalConfig := filepath.Join(scratch, "global.npmrc")
		for _, path := range []string{userConfig, globalConfig} {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				return err
			}
		}
		if target.Kind == "npm" {
			args = append(args, "--userconfig", userConfig, "--globalconfig", globalConfig, "--logs-dir", scratch)
		} else {
			extra = append(extra, "NPM_CONFIG_USERCONFIG="+userConfig, "NPM_CONFIG_GLOBALCONFIG="+globalConfig)
		}
	}
	cmd := exec.CommandContext(ctx, commandPath, args...)
	cmd.Dir = scratch
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + scratch, "XDG_CONFIG_HOME=" + scratch, "XDG_CACHE_HOME=" + scratch, "XDG_DATA_HOME=" + scratch, "LANG=C", "GOENV=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOPATH=" + filepath.Join(scratch, "go"), "GOTELEMETRY=off"}, extra...)
	var stdout, stderr mcpBoundedBuffer
	stdout.limit = 64 * 1024
	stderr.limit = 64 * 1024
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	// A manager can exit while descendants retain pipes or continue modifying its cache.
	if cmd.Process != nil {
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cache manager %s failed", executable)
	}
	if stdout.exceeded || stderr.exceeded {
		return errors.New("cache manager output exceeded safety bound")
	}
	return nil
}
