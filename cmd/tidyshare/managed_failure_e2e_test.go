package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func tidyshareTestBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("TIDYSHARE_TEST_BINARY"); binary != "" {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "tidyshare")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	return binary
}

func managedFailureTool(t *testing.T) (string, func(string, string), func()) {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("owned C uncertainty fixture requires cc")
	}
	bin := t.TempDir()
	program := filepath.Join(bin, "go")
	if output, err := exec.Command(cc, "-Wall", "-Wextra", "-Werror", "testdata/managed_uncertainty.c", "-o", program).CombinedOutput(); err != nil {
		t.Fatalf("C fixture: %v %s", err, output)
	}
	body, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "node"), body, 0o700); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	await := func() {
		once.Do(func() {
			data, _ := os.ReadFile(filepath.Join(bin, "pids"))
			for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				values := strings.Fields(row)
				if len(values) != 3 {
					continue
				}
				until := time.Now().Add(8 * time.Second)
				for {
					gone := true
					for i, value := range values {
						pid, e := strconv.Atoi(value)
						if e != nil || pid <= 0 {
							t.Errorf("invalid fixture PID")
							return
						}
						if i == 2 {
							pid = -pid
						}
						if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
							gone = false
						}
					}
					if gone {
						break
					}
					if time.Now().After(until) {
						t.Errorf("owned fixture did not stop: %s", row)
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		})
	}
	t.Cleanup(await)
	return bin, func(mode, target string) {
		if err := os.WriteFile(filepath.Join(bin, "mode"), []byte(mode+"\n"+target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}, await
}

func checkManagedRetainedRoots(t *testing.T, scratch, diagnostic string, want int) {
	t.Helper()
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != want {
		t.Fatalf("retained roots=%d want %d: %s", len(entries), want, diagnostic)
	}
	for _, entry := range entries {
		root := filepath.Join(scratch, entry.Name())
		st, err := os.Stat(root)
		if err != nil || st.Mode().Perm() != 0o700 || !strings.Contains(diagnostic, root) {
			t.Fatalf("private retained root missing from diagnostic: %s %s", root, diagnostic)
		}
	}
}

// Actual CLI and stdio MCP must refuse to publish a usable uncertain plan.
func TestManagedPlanningUncertaintyCLIAndMCP(t *testing.T) {
	binary := tidyshareTestBinary(t)
	for _, transport := range []string{"cli", "mcp"} {
		t.Run(transport, func(t *testing.T) {
			root, out := inventoryFixture(t)
			write(t, filepath.Join(root, ".cache/go-build/00/fixture-a"), "owned", 48*time.Hour)
			write(t, filepath.Join(root, ".npm/_cacache/content-v2/fixture"), "owned", 48*time.Hour)
			bin, mode, await := managedFailureTool(t)
			defer await()
			mode("qualification", "")
			scratch := t.TempDir()
			environment := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TMPDIR="+scratch)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			diagnostic := ""
			if transport == "cli" {
				command := exec.CommandContext(ctx, binary, "managed-plan", "-root", root, "-out", out, "-kind", "go_build", "-kind", "npm")
				command.Env = environment
				output, err := command.CombinedOutput()
				diagnostic = string(output)
				if err == nil {
					t.Fatalf("uncertain CLI plan succeeded: %s", output)
				}
				if strings.Contains(diagnostic, "managed plan SHA-256:") {
					t.Fatalf("usable digest exposed: %s", output)
				}
			} else {
				command := exec.CommandContext(ctx, binary, "mcp", "-root", root, "-out", out)
				command.Env = environment
				client := mcp.NewClient(&mcp.Implementation{Name: "owned-uncertainty-test", Version: "1"}, nil)
				session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "plan_managed_cleanup", Arguments: map[string]any{"kinds": []string{"go_build", "npm"}}})
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(result.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				var operation mcpOperationResult
				if err := json.Unmarshal(data, &operation); err != nil {
					t.Fatal(err)
				}
				diagnostic = operation.Summary
				if !result.IsError || operation.PlanSHA256 != "" || len(operation.Reports) != 0 {
					t.Fatalf("usable MCP plan exposed: %+v", result)
				}
			}
			entries, err := os.ReadDir(out)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("uncertain plan published reports: %v", entries)
			}
			checkManagedRetainedRoots(t, scratch, diagnostic, 2)
			calls, _ := os.ReadFile(filepath.Join(bin, "calls"))
			if strings.Count(string(calls), "\n") != 1 || strings.Contains(string(calls), "node-start") {
				t.Fatalf("next native target observed: %s", calls)
			}
		})
	}
}

func TestManagedExecutionUncertaintyCLI(t *testing.T) {
	binary := tidyshareTestBinary(t)
	root, out := inventoryFixture(t)
	cache := filepath.Join(root, ".cache/go-build")
	write(t, filepath.Join(cache, "00/fixture-a"), "owned", 48*time.Hour)
	later := filepath.Join(root, "Library/Caches/Homebrew/downloads/preserve")
	write(t, later, "preserve", 48*time.Hour)
	bin, mode, await := managedFailureTool(t)
	defer await()
	mode("normal", cache)
	scratch := t.TempDir()
	environment := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TMPDIR="+scratch)
	plan := exec.Command(binary, "managed-plan", "-root", root, "-out", out, "-kind", "go_build", "-kind", "homebrew")
	plan.Env = environment
	if output, err := plan.CombinedOutput(); err != nil {
		t.Fatalf("plan: %v %s", err, output)
	}
	p, data, err := readManagedPlanData(filepath.Join(out, "managed-plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	targets := []string{}
	for _, target := range p.Targets {
		if target.State == "planned" {
			targets = append(targets, target.Path)
		}
	}
	mode("execution", cache)
	executeOut := filepath.Join(t.TempDir(), "result")
	args := []string{"managed-clean", "-plan", filepath.Join(out, "managed-plan.json"), "-out", executeOut, "-plan-sha256", managedDigest(data), "-yes"}
	for _, target := range targets {
		args = append(args, "-target", target)
	}
	command := exec.Command(binary, args...)
	command.Env = environment
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("uncertain execution succeeded: %s", output)
	}
	reportData, err := os.ReadFile(filepath.Join(executeOut, "managed-cleanup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report managedExecutionReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Status != "partial" || !strings.Contains(report.Error, "uncertain") {
		t.Fatalf("execution result: %s %s", reportData, output)
	}
	journal, err := os.ReadFile(filepath.Join(executeOut, "managed-results.jsonl"))
	if err != nil || !strings.Contains(string(journal), `"status":"intent"`) || !strings.Contains(string(journal), `"status":"partial"`) {
		t.Fatalf("journal lost: %s %v", journal, err)
	}
	if _, err := os.Stat(later); err != nil {
		t.Fatal("later target changed")
	}
	if _, err := os.Stat(filepath.Join(cache, "00/fixture-a")); !os.IsNotExist(err) {
		t.Fatal("fixture action did not run")
	}
	checkManagedRetainedRoots(t, scratch, string(output), 1)
}

func TestManagedExecutionUncertaintyMCP(t *testing.T) {
	binary := tidyshareTestBinary(t)
	root, out := inventoryFixture(t)
	cache := filepath.Join(root, ".cache/go-build")
	write(t, filepath.Join(cache, "00/fixture-a"), "owned", 48*time.Hour)
	later := filepath.Join(root, "Library/Caches/Homebrew/downloads/preserve")
	write(t, later, "preserve", 48*time.Hour)
	bin, mode, await := managedFailureTool(t)
	defer await()
	mode("normal", cache)
	scratch := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "mcp", "-root", root, "-out", out, "-allow-cleanup")
	command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TMPDIR="+scratch)
	client := mcp.NewClient(&mcp.Implementation{Name: "owned-execution-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	plan, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "plan_managed_cleanup", Arguments: map[string]any{"kinds": []string{"go_build", "homebrew"}}})
	if err != nil || plan.IsError {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	decode := func(result *mcp.CallToolResult) mcpOperationResult {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var operation mcpOperationResult
		if err := json.Unmarshal(data, &operation); err != nil {
			t.Fatal(err)
		}
		return operation
	}
	reviewed := decode(plan)
	mode("execution", cache)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cleanup_managed", Arguments: map[string]any{"plan_path": filepath.Join(reviewed.ReportDir, "managed-plan.json"), "plan_sha256": reviewed.PlanSHA256, "targets": []string{cache, filepath.Dir(later)}, "execute": true}})
	if err != nil {
		t.Fatal(err)
	}
	operation := decode(result)
	if !result.IsError || len(operation.Reports) == 0 {
		t.Fatalf("uncertain MCP execution succeeded or lost reports: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(operation.ReportDir, "managed-cleanup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report managedExecutionReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Status != "partial" || !strings.Contains(report.Error, "uncertain") {
		t.Fatalf("partial outcome lost: %s", data)
	}
	if _, err := os.Stat(later); err != nil {
		t.Fatal("later target changed")
	}
	checkManagedRetainedRoots(t, scratch, operation.Summary, 1)
}
