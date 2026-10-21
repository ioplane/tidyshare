package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"
)

func TestMCPDeveloperStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "tidyshare")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry-run-only", true: "explicit-execute"}[allow], func(t *testing.T) {
			root, out := t.TempDir(), t.TempDir()
			cache := filepath.Join(root, ".cargo", "registry", "cache")
			payload := filepath.Join(cache, "index", "fixture.crate")
			write(t, payload, "archive-cache", time.Hour)
			credentials := filepath.Join(root, ".cargo", "credentials.toml")
			write(t, credentials, "private-config-sentinel", time.Hour)
			args := []string{"mcp", "-root", root, "-out", out}
			if allow {
				args = append(args, "-allow-cleanup")
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, binary, args...)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			call := func(name string, input map[string]any) *mcp.CallToolResult {
				t.Helper()
				r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			r := call("discover", map[string]any{"formats": "json,jsonl,md"})
			if r.IsError {
				t.Fatalf("discovery failed: %+v", r)
			}
			body, err := json.Marshal(r.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var result mcpOperationResult
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			planPath := filepath.Join(result.ReportDir, "cache-plan.json")
			if r := call("cleanup_caches", map[string]any{"plan_path": planPath}); r.IsError {
				t.Fatalf("cache dry run failed: %+v", r)
			}
			if _, err := os.Stat(payload); err != nil {
				t.Fatal("dry run removed payload", err)
			}
			r = call("cleanup_caches", map[string]any{"plan_path": planPath, "execute": true})
			if r.IsError == allow {
				t.Fatalf("execution gate: allow=%t error=%t", allow, r.IsError)
			}
			_, statErr := os.Stat(payload)
			if allow && !os.IsNotExist(statErr) {
				t.Fatalf("payload remains after execute: %v", statErr)
			}
			if !allow && statErr != nil {
				t.Fatal("opt-in refusal removed payload", statErr)
			}
			if allow {
				if r := call("cleanup_caches", map[string]any{"plan_path": planPath, "execute": true}); r.IsError {
					t.Fatalf("cache replay failed: %+v", r)
				}
			}
			outside := filepath.Join(t.TempDir(), ".cargo", "registry", "cache")
			outsidePayload := filepath.Join(outside, "index", "outside.crate")
			write(t, outsidePayload, "outside-cache-sentinel", time.Hour)
			info, err := os.Stat(outside)
			if err != nil {
				t.Fatal(err)
			}
			st := info.Sys().(*syscall.Stat_t)
			forged, err := json.Marshal(map[string]any{
				"schema_version": 1, "root": filepath.Dir(filepath.Dir(outside)),
				"targets": []any{map[string]any{"kind": "cargo", "path": outside, "device": uint64(st.Dev), "inode": st.Ino, "uid": st.Uid, "bytes": 22, "files": 1, "cleanup": true}},
			})
			if err != nil {
				t.Fatal(err)
			}
			forgedPath := filepath.Join(out, "forged-cache-plan.json")
			if err := os.WriteFile(forgedPath, forged, 0o600); err != nil {
				t.Fatal(err)
			}
			if r := call("cleanup_caches", map[string]any{"plan_path": forgedPath}); !r.IsError {
				t.Fatal("outside cache accepted even for dry run")
			}
			if body, err := os.ReadFile(outsidePayload); err != nil || string(body) != "outside-cache-sentinel" {
				t.Fatalf("outside cache changed: %q %v", body, err)
			}
			if body, err := os.ReadFile(credentials); err != nil || string(body) != "private-config-sentinel" {
				t.Fatalf("credentials changed: %q %v", body, err)
			}
		})
	}
}

func TestMCPCacheCancellationStopsNativeDescendants(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	binary := filepath.Join(t.TempDir(), "tidyshare")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	p := cacheFixture(t, ".cache/pip", "http-v2/payload.body")
	out := t.TempDir()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(out, "cache-plan.json")
	if err := os.WriteFile(planPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Root, "mode"), []byte("cancel"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(p.Root, "ready.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bindir := filepath.Join(t.TempDir(), "cache-native-helper")
	if err := os.Mkdir(bindir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(helper, filepath.Join(bindir, "pip")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bindir)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, binary, "mcp", "-root", p.Root, "-out", out, "-allow-cleanup")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{}, 1)
	go func() {
		_, _ = session.CallTool(request, &mcp.CallToolParams{Name: "cleanup_caches", Arguments: map[string]any{"plan_path": planPath, "execute": true}})
		done <- struct{}{}
	}()
	defer func() { cancel(); <-done }()
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Fscanln(conn, &pid); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Kill(pid, unix.SIGKILL) }()
	cancel()
	var data [1]byte
	if _, err := conn.Read(data[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("MCP cancellation left a native descendant running: %v", err)
	}
}
