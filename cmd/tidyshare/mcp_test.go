package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Removing the scope checks must expose an outside plan or scan and fail this test.
func TestMCPBoundaries(t *testing.T) {
	root, out := filepath.Join(t.TempDir(), "data"), t.TempDir()
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ops, err := newMCPOperations(mcpOptions{root: root, out: out, imageOwner: "root"})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "child")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(outside), "mcp-escape"), 0o700); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Rename(filepath.Join(filepath.Dir(outside), "mcp-escape"), filepath.Join(outside, "escape"))
	})
	for _, path := range []string{outside, filepath.Join(root, ".."), filepath.Join(root, "alias"), root + "/alias/../mcp-escape"} {
		if _, err := ops.scan(context.Background(), mcpScanInput{Path: path}); err == nil {
			t.Errorf("unsafe scan accepted: %s", path)
		}
	}
	if _, err := ops.cleanup(context.Background(), "duplicates", mcpCleanupInput{PlanPath: filepath.Join(root, "missing"), Execute: true}); err == nil {
		t.Fatal("execution without opt-in accepted")
	}
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(root+"-moved", root) })
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(root, root+"-replacement") })
	if _, err := ops.scan(context.Background(), mcpScanInput{}); err == nil {
		t.Fatal("replaced root accepted")
	}
}

// This runs the actual command and therefore also catches stdout protocol noise.
func TestMCPStdioIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "tidyshare")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry-run-only", true: "cleanup-enabled"}[allow], func(t *testing.T) {
			root, out := t.TempDir(), t.TempDir()
			write(t, filepath.Join(root, "keep"), "same-content", 48*time.Hour)
			write(t, filepath.Join(root, "duplicate"), "same-content", 48*time.Hour)
			fixture := &cleanupImageFixture{images: "[" + fixtureOrphan(strings.Repeat("a", 64)) + "]"}
			owner, socket := cleanupImageAPI(t, fixture)
			args := []string{"mcp", "-root", root, "-out", out, "-image-owner", owner, "-image-socket", socket}
			if allow {
				args = append(args, "-allow-cleanup")
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, binary, args...)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools.Tools) != 4 {
				t.Fatalf("tools = %d", len(tools.Tools))
			}
			for _, tool := range tools.Tools {
				if tool.Annotations == nil {
					t.Fatalf("missing annotation: %s", tool.Name)
				}
			}
			call := func(name string, arguments map[string]any) *mcp.CallToolResult {
				t.Helper()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			scan := call("scan", map[string]any{"min_size": 1, "formats": "json,md"})
			if scan.IsError {
				t.Fatalf("scan failed: %+v", scan)
			}
			raw, err := json.Marshal(scan.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var result mcpOperationResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(result.ReportDir, out+"/") || len(result.Reports) == 0 {
				t.Fatalf("unexpected result: %+v", result)
			}
			plan := filepath.Join(result.ReportDir, "dups.tsv")
			dry := call("cleanup_duplicates", map[string]any{"plan_path": plan})
			if dry.IsError {
				t.Fatalf("dry-run failed: %+v", dry)
			}
			for _, name := range []string{"keep", "duplicate"} {
				if _, err := os.Stat(filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			execute := call("cleanup_duplicates", map[string]any{"plan_path": plan, "execute": true, "limit": 1})
			if execute.IsError == allow {
				t.Fatalf("execution gate: allow=%v error=%v", allow, execute.IsError)
			}
			files, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if allow {
				want = 1
			}
			if len(files) != want {
				t.Fatalf("files=%d want %d", len(files), want)
			}
			if allow {
				outsidePath := filepath.Join(t.TempDir(), "outside")
				write(t, outsidePath, "same-content", 48*time.Hour)
				insidePath := filepath.Join(root, files[0].Name())
				for _, outsideKeeper := range []bool{true, false} {
					keepPath, dupPath := insidePath, outsidePath
					if outsideKeeper {
						keepPath, dupPath = outsidePath, insidePath
					}
					keepInfo, err := os.Stat(keepPath)
					if err != nil {
						t.Fatal(err)
					}
					dupInfo, err := os.Stat(dupPath)
					if err != nil {
						t.Fatal(err)
					}
					badPath := filepath.Join(out, "outside.tsv")
					write(t, badPath, planHeader+fmt.Sprintf("1\tkeep\t12\t%d\t%q\n1\tdup\t12\t%d\t%q\n", keepInfo.ModTime().Unix(), keepPath, dupInfo.ModTime().Unix(), dupPath), 0)
					refused := call("cleanup_duplicates", map[string]any{"plan_path": badPath, "execute": true})
					if !refused.IsError {
						t.Fatalf("outside plan accepted: keeper=%v", outsideKeeper)
					}
					for _, path := range []string{insidePath, outsidePath} {
						if _, err := os.Stat(path); err != nil {
							t.Fatalf("outside plan removed %s: %v", path, err)
						}
					}
				}
			}
			if allow {
				repeat := call("cleanup_duplicates", map[string]any{"plan_path": plan, "execute": true})
				if repeat.IsError {
					t.Fatalf("repeat cleanup failed: %+v", repeat)
				}
			}
			imagePlan := call("plan_images", map[string]any{"formats": "json"})
			if imagePlan.IsError {
				t.Fatalf("image plan failed: %+v", imagePlan)
			}
			imageRaw, err := json.Marshal(imagePlan.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var imageResult mcpOperationResult
			if err := json.Unmarshal(imageRaw, &imageResult); err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(imageResult.ReportDir, "image-plan.json")
			imageDry := call("cleanup_images", map[string]any{"plan_path": imagePath})
			if imageDry.IsError {
				t.Fatalf("image dry run failed: %+v", imageDry)
			}
			fixture.mu.Lock()
			deletesBefore := len(fixture.deletes)
			fixture.mu.Unlock()
			if deletesBefore != 0 {
				t.Fatal("image dry run removed image")
			}
			imageExecute := call("cleanup_images", map[string]any{"plan_path": imagePath, "execute": true, "limit": 1})
			if imageExecute.IsError == allow {
				t.Fatalf("image execution gate: allow=%v error=%v", allow, imageExecute.IsError)
			}
			fixture.mu.Lock()
			deletesAfter := len(fixture.deletes)
			fixture.mu.Unlock()
			wantDeletes := 0
			if allow {
				wantDeletes = 1
			}
			if deletesAfter != wantDeletes {
				t.Fatalf("image deletions=%d want %d", deletesAfter, wantDeletes)
			}
			fixture.mu.Lock()
			fixture.images = "private-upstream-response-marker"
			fixture.mu.Unlock()
			failed := call("plan_images", map[string]any{})
			if !failed.IsError {
				t.Fatal("malformed API response accepted")
			}
			failureRaw, err := json.Marshal(failed.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var failureResult mcpOperationResult
			if err := json.Unmarshal(failureRaw, &failureResult); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(failureResult.ReportDir, out+"/") {
				t.Fatalf("failure report path missing: %s", failureRaw)
			}
			failureContent, err := json.Marshal(failed)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(failureContent), "private-upstream-response-marker") {
				t.Fatal("upstream response leaked")
			}
			outside := call("scan", map[string]any{"path": t.TempDir()})
			if !outside.IsError {
				t.Fatal("outside scan accepted")
			}
		})
	}
	t.Run("cancel-subprocess", func(t *testing.T) {
		root, out := t.TempDir(), t.TempDir()
		socket := filepath.Join(t.TempDir(), "blocked.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		accepted := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			defer connection.Close()
			if _, err := bufio.NewReader(connection).ReadString('\n'); err == nil {
				close(accepted)
			}
			<-ctx.Done()
		}()
		operations, err := newMCPOperations(mcpOptions{root: root, out: out, imageOwner: "root", imageSocket: "unix://" + socket})
		if err != nil {
			t.Fatal(err)
		}
		operations.executable = binary
		operationCtx, stop := context.WithCancel(ctx)
		defer stop()
		done := make(chan error, 1)
		go func() { _, err := operations.planImages(operationCtx, mcpPlanInput{}); done <- err }()
		select {
		case <-accepted:
		case <-ctx.Done():
			t.Fatal("child did not reach API")
		}
		stop()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "cancelled") {
				t.Fatalf("cancellation: %v", err)
			}
		case <-timer.C:
			t.Fatal("child was not collected after cancellation")
		}
		cancel()
		<-stopped
	})
}

func TestMCPOutputRemainsBounded(t *testing.T) {
	output := mcpBoundedBuffer{limit: 3}
	for _, chunk := range []string{"ab", "cdef", "again"} {
		n, err := output.Write([]byte(chunk))
		if n != len(chunk) || err != nil {
			t.Fatalf("writer failed: %d %v", n, err)
		}
	}
	if !output.exceeded || string(output.data) != "abc" {
		t.Fatalf("unbounded output: %q", string(output.data))
	}
}

func TestMCPRejectsOverlappingReportDirectories(t *testing.T) {
	base := t.TempDir()
	for _, paths := range [][2]string{{base, base}, {base, filepath.Join(base, "reports")}, {filepath.Join(base, "data"), base}} {
		if _, err := newMCPOperations(mcpOptions{root: paths[0], out: paths[1]}); err == nil {
			t.Errorf("overlapping directories accepted: %v", paths)
		}
	}
}
