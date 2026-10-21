package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCacheCleanupDryRunClearRepeat(t *testing.T) {
	p := cacheFixture(t, ".cargo/registry/cache", "index/payload.crate")
	r, err := cleanupCaches(context.Background(), p, false, 0)
	if err != nil || len(r.Results) != 1 || r.Results[0].Status != "would_clear" || r.Changed != 0 {
		t.Fatalf("dry run: %+v %v", r, err)
	}
	r, err = cleanupCaches(context.Background(), p, true, 1)
	if err != nil || r.Changed != 1 || r.LogicalBytes != 6 {
		t.Fatalf("clear: %+v %v", r, err)
	}
	r, err = cleanupCaches(context.Background(), p, true, 0)
	if err != nil || r.Changed != 0 || r.Results[0].Status != "already_empty" {
		t.Fatalf("repeat: %+v %v", r, err)
	}
}

func TestCacheCleanupRefusesBusyPayloadAndCWD(t *testing.T) {
	p := cacheFixture(t, ".cargo/registry/cache", "index/payload.crate")
	f, err := os.Open(filepath.Join(p.Targets[0].Path, "index/payload.crate"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := cleanupCaches(context.Background(), p, true, 0)
	_ = f.Close()
	if err != nil || r.Changed != 0 || r.Results[0].Status != "skipped" {
		t.Fatalf("open payload: %+v %v", r, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(p.Targets[0].Path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	r, err = cleanupCaches(context.Background(), p, true, 0)
	if err != nil || r.Changed != 0 || r.Results[0].Status != "skipped" {
		t.Fatalf("cwd: %+v %v", r, err)
	}
}

func TestCacheCleanupRefusesChangedIdentityAndSource(t *testing.T) {
	for _, mode := range []string{"identity", "git", "config", "source", "symlink", "archive"} {
		t.Run(mode, func(t *testing.T) {
			p := cacheFixture(t, ".cargo/registry/cache", "index/payload.crate")
			path := p.Targets[0].Path
			switch mode {
			case "identity":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "git":
				if err := os.Mkdir(filepath.Join(path, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "config":
				if err := os.WriteFile(filepath.Join(path, "config.toml"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "source":
				if err := os.WriteFile(filepath.Join(path, "main.go"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(path, "link")); err != nil {
					t.Fatal(err)
				}
			case "archive":
				if err := os.WriteFile(filepath.Join(path, "index/not-an-archive"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r, err := cleanupCaches(context.Background(), p, true, 0)
			if err != nil || r.Changed != 0 || r.Results[0].Status != "skipped" {
				t.Fatalf("%s: %+v %v", mode, r, err)
			}
		})
	}
}

func TestCacheCleanupWholePlanAndCancellation(t *testing.T) {
	p := cacheFixture(t, ".cargo/registry/cache", "index/payload.crate")
	bad := p.Targets[0]
	bad.Path = p.Root
	p.Targets = append(p.Targets, bad)
	if _, err := cleanupCaches(context.Background(), p, true, 0); err == nil {
		t.Fatal("invalid tail accepted")
	}
	if _, err := os.Stat(filepath.Join(p.Targets[0].Path, "index/payload.crate")); err != nil {
		t.Fatal("first target changed before whole validation")
	}
	p.Targets = p.Targets[:1]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cleanupCaches(ctx, p, true, 0); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestCacheCleanupMissingLimitAndUnsupported(t *testing.T) {
	p := cacheFixture(t, ".cargo/registry/cache", "index/payload.crate")
	if err := os.Rename(p.Targets[0].Path, p.Targets[0].Path+".old"); err != nil {
		t.Fatal(err)
	}
	r, err := cleanupCaches(context.Background(), p, true, 0)
	if err != nil || r.Results[0].Status != "absent" {
		t.Fatalf("missing: %+v %v", r, err)
	}
	p = cacheFixture(t, ".cache/pip", "http-v2/payload.body")
	t.Setenv("PATH", t.TempDir())
	r, err = cleanupCaches(context.Background(), p, true, 0)
	if err != nil || r.Changed != 0 || r.Skipped != 1 {
		t.Fatalf("missing tool: %+v %v", r, err)
	}
}

func TestCacheCleanupUVNativeSymlinkSentinel(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv is unavailable")
	}
	p := cacheFixture(t, ".cache/uv", "archive-v0/payload.bin")
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.Targets[0].Path, "archive-v0/external")); err != nil {
		t.Fatal(err)
	}
	r, err := cleanupCaches(context.Background(), p, true, 0)
	if err != nil || r.Changed != 1 {
		t.Fatalf("uv clean: %+v %v", r, err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve" {
		t.Fatalf("uv followed external symlink: %q %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(p.Targets[0].Path, "archive-v0/external")); !os.IsNotExist(err) {
		t.Fatalf("symlink retained: %v", err)
	}
}

func TestCacheCleanupNativeFailureIsError(t *testing.T) {
	p := cacheFixture(t, ".cache/pip", "http-v2/payload.body")
	bindir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bindir, "pip"), []byte("#!/bin/sh\nexit 42\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bindir)
	r, err := cleanupCaches(context.Background(), p, true, 0)
	if err == nil || r.Changed != 0 || len(r.Results) != 1 {
		t.Fatalf("native failure: %+v %v", r, err)
	}
}

func TestCacheCleanupProtectsNativeCacheRootSource(t *testing.T) {
	for _, layout := range []string{".cache/uv", "go/pkg/mod", ".nuget/packages"} {
		for _, marker := range []string{".git", "go.mod", "pyproject.toml", "main.go"} {
			t.Run(layout+"/"+marker, func(t *testing.T) {
				p := cacheFixture(t, layout, marker)
				r, err := cleanupCaches(context.Background(), p, true, 0)
				if err != nil || r.Changed != 0 || r.Skipped != 1 {
					t.Fatalf("native source root: %+v %v", r, err)
				}
			})
		}
	}
}

func TestCacheCleanupClearsSeveralDirectoryBatches(t *testing.T) {
	p := cacheFixture(t, ".cargo/registry/cache", "index/first.crate")
	for i := range 300 {
		if err := os.WriteFile(filepath.Join(p.Targets[0].Path, "index", fmt.Sprintf("%03d.crate", i)), []byte("cached"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := cleanupCaches(context.Background(), p, true, 0)
	if err != nil || r.Changed != 1 || r.LogicalBytes != 301*6 {
		t.Fatalf("batched clear: %+v %v", r, err)
	}
	r, err = cleanupCaches(context.Background(), p, true, 0)
	if err != nil || r.Changed != 0 || r.Results[0].Status != "already_empty" {
		t.Fatalf("batch repeat: %+v %v", r, err)
	}
}

// The installed test executable acts as a native manager without a shell.
func init() {
	if len(os.Args) == 3 && os.Args[1] == "--cache-native-descendant" {
		conn, err := net.Dial("unix", os.Args[2])
		if err != nil {
			os.Exit(81)
		}
		if _, err := fmt.Fprintln(conn, os.Getpid()); err != nil {
			os.Exit(82)
		}
		var stop [1]byte
		_, _ = conn.Read(stop[:])
		_ = conn.Close()
		os.Exit(0)
	}
	if filepath.Base(os.Args[0]) != "pip" || filepath.Base(filepath.Dir(os.Args[0])) != "cache-native-helper" {
		return
	}
	var cache string
	for i, arg := range os.Args[:len(os.Args)-1] {
		if arg == "--cache-dir" {
			cache = os.Args[i+1]
		}
	}
	if cache == "" {
		os.Exit(83)
	}
	root := filepath.Dir(filepath.Dir(cache))
	if err := os.Remove(filepath.Join(cache, "http-v2/payload.body")); err != nil {
		os.Exit(84)
	}
	child := exec.Command(os.Args[0], "--cache-native-descendant", filepath.Join(root, "ready.sock"))
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(85)
	}
	mode, err := os.ReadFile(filepath.Join(root, "mode"))
	if err != nil {
		os.Exit(86)
	}
	if string(mode) == "exit" {
		os.Exit(0)
	}
	if err := child.Wait(); err != nil {
		os.Exit(87)
	}
	os.Exit(0)
}

func TestCacheCleanupStopsNativeDescendants(t *testing.T) {
	for _, mode := range []string{"cancel", "exit"} {
		t.Run(mode, func(t *testing.T) {
			p := cacheFixture(t, ".cache/pip", "http-v2/payload.body")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(p.Root, "ready.sock"), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p.Root, "mode"), []byte(mode), 0o600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			bindir := filepath.Join(t.TempDir(), "cache-native-helper")
			if err := os.Mkdir(bindir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(binary, filepath.Join(bindir, "pip")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bindir)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				report cleanupReport
				err    error
			}
			done := make(chan outcome, 1)
			go func() { report, err := cleanupCaches(ctx, p, true, 0); done <- outcome{report, err} }()
			// The child keeps stdout/stderr open even when its manager exits.
			conn, err := listener.AcceptUnix()
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var pid int
			if _, err := fmt.Fscanln(conn, &pid); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Kill(pid, unix.SIGKILL) }()
			if _, err := os.Stat(filepath.Join(p.Targets[0].Path, "http-v2/payload.body")); !os.IsNotExist(err) {
				t.Fatalf("helper did not make its partial change: %v", err)
			}
			if mode == "cancel" {
				cancel()
			}
			var result outcome
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("native cleanup did not return within its bound")
			}
			if result.err == nil {
				t.Fatalf("expected interrupted/failed command: %+v", result.report)
			}
			var data [1]byte
			if _, err := conn.Read(data[:]); !errors.Is(err, io.EOF) {
				t.Fatalf("native descendant survived its manager: %v", err)
			}
			if mode == "cancel" && (len(result.report.Results) != 1 || result.report.Results[0].Status != "interrupted" || result.report.Results[0].Reason == "") {
				t.Fatalf("partial cancellation reported as unchanged: %+v", result.report)
			}
		})
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

func cacheFixtureSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			snapshot[path] = "directory"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[path] = string(content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestCacheCleanupProtectsBareGitRepositories(t *testing.T) {
	for _, layout := range []string{".gradle/caches", ".cargo/registry/cache", "go/pkg/mod", ".cache/go-build", ".cache/uv", ".cache/pip", ".npm/_cacache", ".local/share/pnpm/store", ".nuget/packages", ".local/share/NuGet/v3-cache"} {
		for _, nested := range []string{"", "ordinary/repository", "archive-v0/repository", "git-v0/db/ordinary"} {
			for _, execute := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/nested=%v/execute=%v", layout, nested != "", execute), func(t *testing.T) {
					p := cacheFixture(t, layout, "payload.crate")
					cacheBareGitFixture(t, filepath.Join(p.Targets[0].Path, nested))
					before := cacheFixtureSnapshot(t, p.Root)
					report, err := cleanupCaches(context.Background(), p, execute, 0)
					if err != nil || len(report.Results) != 1 || report.Results[0].Status != "skipped" || report.Changed != 0 {
						t.Fatalf("bare repo is actionable: %+v %v", report, err)
					}
					if after := cacheFixtureSnapshot(t, p.Root); !reflect.DeepEqual(after, before) {
						t.Fatal("protected bare repository or preceding cache payload changed")
					}
				})
			}
		}
	}
}

func TestCacheCleanupAllowsOnlyManagedBareGitPaths(t *testing.T) {
	for _, fixture := range []struct{ layout, nested string }{
		{"go/pkg/mod", "cache/vcs/" + strings.Repeat("a", 64)},
		{".cache/uv", "git-v0/db/" + strings.Repeat("b", 16)},
		{".cache/uv", "git-v1/db/" + strings.Repeat("c", 16)},
	} {
		t.Run(fixture.nested, func(t *testing.T) {
			p := cacheFixture(t, fixture.layout, "payload.crate")
			cacheBareGitFixture(t, filepath.Join(p.Targets[0].Path, fixture.nested))
			before := cacheFixtureSnapshot(t, p.Root)
			report, err := cleanupCaches(context.Background(), p, false, 0)
			if err != nil || len(report.Results) != 1 || report.Results[0].Status != "would_clear" {
				t.Fatalf("native-owned VCS cache rejected: %+v %v", report, err)
			}
			if !reflect.DeepEqual(cacheFixtureSnapshot(t, p.Root), before) {
				t.Fatal("dry run changed managed VCS cache")
			}
		})
	}
}

func TestCacheCleanupBusyRawProcPaths(t *testing.T) {
	for _, literal := range []string{`literal\040space`, `literal\011tab`, `literal\012newline`, `literal\134backslash`} {
		for _, execute := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/execute=%v", literal, execute), func(t *testing.T) {
				p := cacheFixture(t, filepath.Join(literal, ".cargo/registry/cache"), "index/payload.crate")
				file, err := os.Open(filepath.Join(p.Targets[0].Path, "index/payload.crate"))
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				before := cacheFixtureSnapshot(t, p.Root)
				report, err := cleanupCaches(context.Background(), p, execute, 0)
				if err != nil || len(report.Results) != 1 || report.Results[0].Status != "skipped" || !strings.Contains(report.Results[0].Reason, "in use") || report.Changed != 0 {
					t.Fatalf("busy literal path accepted: %+v %v", report, err)
				}
				if !reflect.DeepEqual(cacheFixtureSnapshot(t, p.Root), before) {
					t.Fatal("busy literal payload changed")
				}
			})
		}
	}
}

func TestCacheCleanupBusyLiteralDeletedDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "uv (deleted)")
	if err := os.MkdirAll(filepath.Join(path, "archive-v0"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "archive-v0/payload.bin"), []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UV_CACHE_DIR", path)
	target, err := cacheTargetAt(context.Background(), path)
	if err != nil || target == nil {
		t.Fatalf("custom target: %+v %v", target, err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	p := cachePlan{SchemaVersion: 1, Root: root, Targets: []cacheTarget{*target}}
	report, err := cleanupCaches(context.Background(), p, false, 0)
	if err != nil || len(report.Results) != 1 || report.Results[0].Status != "skipped" || !strings.Contains(report.Results[0].Reason, "in use") {
		t.Fatalf("literal deleted directory accepted: %+v %v", report, err)
	}
}

func TestCacheCleanupNativeNPMConfigurationIsolation(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm is unavailable")
	}
	p := cacheFixture(t, ".npm/_cacache", "content-v2/sha512/aa/payload")
	protected := filepath.Join(p.Root, "protected-cache")
	if err := os.Mkdir(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(protected, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(p.Root, "poison.npmrc")
	if err := os.WriteFile(config, []byte("cache="+protected+"\n_auth=must-not-load\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executed := filepath.Join(p.Root, "executed")
	hook := filepath.Join(p.Root, "hook.cjs")
	if err := os.WriteFile(hook, fmt.Appendf(nil, "require('fs').writeFileSync(%q, 'unexpected')", executed), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NPM_CONFIG_USERCONFIG", config)
	t.Setenv("NPM_CONFIG_GLOBALCONFIG", config)
	t.Setenv("NPM_CONFIG_CACHE", protected)
	t.Setenv("NODE_OPTIONS", "--require="+hook)
	report, err := cleanupCaches(context.Background(), p, true, 0)
	if err != nil || report.Changed != 1 || report.Skipped != 0 {
		t.Fatalf("native npm configuration isolation: %+v %v", report, err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "preserve" {
		t.Fatalf("inherited cache selected: %q %v", content, err)
	}
	if _, err := os.Stat(executed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inherited node hook executed: %v", err)
	}
}
