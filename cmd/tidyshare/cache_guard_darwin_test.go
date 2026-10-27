//go:build darwin && cgo

package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func idleDarwinAPI() darwinProcAPI {
	return darwinProcAPI{
		query: func(kind, pid int, arg uint64) (darwinProcResult, error) {
			switch kind {
			case darwinIdentity:
				return darwinProcResult{start: 1}, nil
			case darwinDirectories:
				return darwinProcResult{path: "/outside", root: "/"}, nil
			case darwinExecutable:
				return darwinProcResult{path: "/outside/program"}, nil
			case darwinRegion:
				if arg == 0 {
					return darwinProcResult{address: 4096, size: 4096}, nil
				}
				return darwinProcResult{}, unix.EINVAL
			default:
				return darwinProcResult{path: "/outside/file"}, nil
			}
		},
		list: func(kind, pid int, out []uint64) (int, error) { return 0, nil },
	}
}

func TestDarwinListRejectsErrorsAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(int, int, []uint64) (int, error)
	}{
		{"permission", func(int, int, []uint64) (int, error) { return 0, unix.EPERM }},
		{"full", func(_ int, _ int, b []uint64) (int, error) { return len(b), nil }},
		{"invalid", func(int, int, []uint64) (int, error) { return -1, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := darwinReadList(darwinPIDs, 0, tc.call); err == nil {
				t.Fatal("incomplete process inventory was accepted")
			}
		})
	}
}

func TestDarwinProcessRejectsIncompleteInspection(t *testing.T) {
	for _, kind := range []int{darwinIdentity, darwinDirectories, darwinExecutable, darwinRegion} {
		for _, failure := range []error{unix.EPERM, unix.ESRCH} {
			api := idleDarwinAPI()
			base := api.query
			api.query = func(k, p int, a uint64) (darwinProcResult, error) {
				if k == kind {
					return darwinProcResult{}, failure
				}
				return base(k, p, a)
			}
			if err := darwinCheckProcess(context.Background(), "/cache", 42, nil, api); !errors.Is(err, failure) {
				t.Errorf("kind %d: expected %v, got %v", kind, failure, err)
			}
		}
	}
}

func TestDarwinProcessRejectsReuse(t *testing.T) {
	api := idleDarwinAPI()
	base := api.query
	calls := uint64(0)
	api.query = func(k, p int, a uint64) (darwinProcResult, error) {
		if k == darwinIdentity {
			calls++
			return darwinProcResult{start: calls}, nil
		}
		return base(k, p, a)
	}
	if err := darwinCheckProcess(context.Background(), "/cache", 42, nil, api); err == nil {
		t.Fatal("reused PID was accepted")
	}
}

func TestDarwinProcessRejectsIncompleteRegions(t *testing.T) {
	for _, result := range []darwinProcResult{{address: 4096, size: 0}, {address: 0, size: 1}, {address: ^uint64(0), size: 2}} {
		api := idleDarwinAPI()
		base := api.query
		api.query = func(k, p int, a uint64) (darwinProcResult, error) {
			if k == darwinRegion {
				return result, nil
			}
			return base(k, p, a)
		}
		if err := darwinCheckProcess(context.Background(), "/cache", 42, nil, api); err == nil {
			t.Fatal("incomplete region traversal was accepted")
		}
	}
}

func TestDarwinProcessDetectsPaths(t *testing.T) {
	for _, kind := range []int{darwinDirectories, darwinExecutable, darwinFDPath, darwinFileportPath, darwinRegion} {
		api := idleDarwinAPI()
		base := api.query
		api.query = func(k, p int, a uint64) (darwinProcResult, error) {
			r, e := base(k, p, a)
			if k == kind {
				r.path = "/cache/payload"
			}
			return r, e
		}
		api.list = func(k, p int, b []uint64) (int, error) { b[0] = uint64(1)<<32 | 7; return 1, nil }
		if err := darwinCheckProcess(context.Background(), "/cache", 42, nil, api); err == nil || !strings.Contains(err.Error(), "in use") {
			t.Errorf("kind %d: expected busy, got %v", kind, err)
		}
	}
}

func TestDarwinProcessCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := darwinCheckProcess(ctx, "/cache", 42, nil, idleDarwinAPI()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestDarwinProcessRejectsUnresolvedMapping(t *testing.T) {
	api := idleDarwinAPI()
	base := api.query
	api.query = func(k, p int, a uint64) (darwinProcResult, error) {
		r, err := base(k, p, a)
		if k == darwinRegion && err == nil {
			r.flags = 1
		}
		return r, err
	}
	if err := darwinCheckProcess(context.Background(), "/cache", 42, nil, api); err == nil {
		t.Fatal("unresolved mapping was accepted")
	}
}

func TestDarwinOnlyExplicitOwnDescriptorsAreExcluded(t *testing.T) {
	for _, tc := range []struct {
		pid  int
		own  []uintptr
		busy bool
	}{
		{os.Getpid(), []uintptr{7}, false},
		{os.Getpid(), []uintptr{8}, true},
		{os.Getpid() + 1, []uintptr{7}, true},
	} {
		api := idleDarwinAPI()
		base := api.query
		api.query = func(k, p int, a uint64) (darwinProcResult, error) {
			r, err := base(k, p, a)
			if k == darwinFDPath {
				r.path = "/cache/file"
			}
			return r, err
		}
		api.list = func(k, p int, b []uint64) (int, error) {
			if k == darwinFDs {
				b[0] = 1<<32 | 7
				return 1, nil
			}
			return 0, nil
		}
		err := darwinCheckProcess(context.Background(), "/cache", tc.pid, tc.own, api)
		if tc.busy && (err == nil || !strings.Contains(err.Error(), "in use")) {
			t.Fatalf("expected busy, got %v", err)
		}
		if !tc.busy && err != nil {
			t.Fatal(err)
		}
	}
}

func TestDarwinMountGuardRejectsNestedAndIncomplete(t *testing.T) {
	for _, mode := range []string{"nested", "full", "permission", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			call := func(out []unix.Statfs_t, flags int) (int, error) {
				if len(out) == 0 {
					return 1, nil
				}
				switch mode {
				case "permission":
					return 0, unix.EPERM
				case "full":
					return len(out), nil
				case "invalid":
					return -1, nil
				default:
					copy(out[0].Mntonname[:], "/cache/nested")
					return 1, nil
				}
			}
			if err := darwinMountGuard("/cache", call); err == nil {
				t.Fatal("unsafe mount inventory was accepted")
			}
		})
	}
}

const darwinProcessFixture = `
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/fileport.h>
#include <sys/mman.h>
#include <unistd.h>
int main(int argc, char **argv) {
    if (argc != 3) return 2;
    int fd = open(argv[2], O_RDONLY);
    if (fd < 0) return 3;
    if (!strcmp(argv[1], "cwd") && fchdir(fd)) return 4;
    if (!strcmp(argv[1], "mmap") && mmap(0, 4096, PROT_READ, MAP_PRIVATE, fd, 0) == MAP_FAILED) return 5;
    if (!strcmp(argv[1], "fileport")) {
        fileport_t port;
        if (fileport_makeport(fd, &port)) return 6;
    }
    if (strcmp(argv[1], "fd")) close(fd);
    puts("ready"); fflush(stdout);
    getchar();
    return 0;
}
`

func startDarwinFixture(t *testing.T, binary, mode, path string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cmd := exec.CommandContext(ctx, binary, mode, path)
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); _ = cmd.Wait(); cancel() })
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("fixture failed: %q, %v", line, err)
	}
	return cmd.Process.Pid
}

func TestDarwinNativeProcessReferences(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "fixture.c")
	binary := filepath.Join(base, "fixture")
	if err := os.WriteFile(source, []byte(darwinProcessFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cc", "-Wall", "-Werror", source, "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v\n%s", err, out)
	}
	for _, mode := range []string{"fd", "cwd", "mmap", "fileport", "executable"} {
		t.Run(mode, func(t *testing.T) {
			cache := filepath.Join(base, mode)
			if err := os.Mkdir(cache, 0o700); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(cache, "payload")
			if err := os.WriteFile(payload, make([]byte, 4096), 0o600); err != nil {
				t.Fatal(err)
			}
			executable := binary
			if mode == "cwd" {
				payload = cache
			}
			if mode == "executable" {
				data, err := os.ReadFile(binary)
				if err != nil {
					t.Fatal(err)
				}
				executable = filepath.Join(cache, "program")
				if err := os.WriteFile(executable, data, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			pid := startDarwinFixture(t, executable, mode, payload)
			err := darwinCheckProcess(context.Background(), cache, pid, nil, nativeDarwinAPI)
			if err == nil || !strings.Contains(err.Error(), "in use") {
				t.Fatalf("expected busy %s, got %v", mode, err)
			}
		})
	}
}

func TestDarwinNativeOwnDescriptorExclusion(t *testing.T) {
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := darwinCheckProcess(context.Background(), path, os.Getpid(), nil, nativeDarwinAPI); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("own descriptor was not detected: %v", err)
	}
	if err := darwinCheckProcess(context.Background(), path, os.Getpid(), []uintptr{first.Fd()}, nativeDarwinAPI); err != nil {
		if !errors.Is(err, unix.ENOTSUP) {
			t.Fatalf("explicit own descriptor exclusion: %v", err)
		}
		t.Logf("descriptor excluded, but idle qualification remains blocked: %v", err)
	}
	second, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := darwinCheckProcess(context.Background(), path, os.Getpid(), []uintptr{first.Fd()}, nativeDarwinAPI); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("unsupplied own descriptor was ignored: %v", err)
	}
}

func TestDarwinNativeMountGuard(t *testing.T) {
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := cacheMountGuard(path); err != nil {
		t.Fatal(err)
	}
	if err := cacheMountGuard("/"); err == nil {
		t.Fatal("nested system mounts were not detected")
	}
}

func TestDarwinHostIdleQualification(t *testing.T) {
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := cacheBusy(context.Background(), path, nil); err != nil {
		t.Skipf("idle-cache qualification blocked: %v", err)
	}
}

func TestDarwinNativeInventoryIncludesOtherUsers(t *testing.T) {
	pids, err := darwinReadList(darwinPIDs, 0, nativeDarwinAPI.list)
	if err != nil {
		t.Fatal(err)
	}
	var init, self bool
	for _, pid := range pids {
		init = init || pid == 1
		self = self || pid == uint64(os.Getpid())
	}
	if !init || !self {
		t.Fatalf("process inventory omitted init or self: init=%v self=%v", init, self)
	}
	if _, err := nativeDarwinAPI.query(darwinDirectories, 1, 0); err != nil {
		t.Logf("system process visibility blocks idle qualification: PID 1: %v", err)
	}
}
