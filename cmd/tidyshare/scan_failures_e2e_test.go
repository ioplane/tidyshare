package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func partialScanStage(t *testing.T, parent string) string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "tidyshare-reports-") {
			stages = append(stages, filepath.Join(parent, entry.Name()))
		}
	}
	if len(stages) != 1 {
		t.Fatalf("private partial stage missing: %v", entries)
	}
	st, err := os.Stat(stages[0])
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("partial stage is not private: %v", err)
	}
	return stages[0]
}

func TestScanYaraFailureCLIPreservesPrivateEvidence(t *testing.T) {
	binary := tidyshareTestBinary(t)
	entries, o := yaraFixture(t, `printf 'fixture %s\n' "$TIDYSHARE_YARA_TARGET"; echo owned-diagnostic >&2; exit 9`)
	scratch, report := t.TempDir(), filepath.Join(t.TempDir(), "reports")
	command := exec.Command(binary, "scan", "-root", filepath.Dir(entries[0].Path), "-out", report, "-images=false", "-min-size", "1", "-settle", "0", "-workers", "1", "-yara-rules", o.yaraRules, "-yara-bin", o.yaraBin)
	command.Env = append(os.Environ(), "TMPDIR="+scratch)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("requested YARA failure returned success: %s", output)
	}
	stage := partialScanStage(t, scratch)
	if !strings.Contains(string(output), stage) {
		t.Fatalf("retained path missing: %s", output)
	}
	if _, err := os.Stat(filepath.Join(report, "report.json")); !os.IsNotExist(err) {
		t.Fatal("failed scan published a success report")
	}
	for name, marker := range map[string]string{"yara.tsv": "fixture", "yara.err": "owned-diagnostic"} {
		data, err := os.ReadFile(filepath.Join(stage, name))
		if err != nil || !strings.Contains(string(data), marker) {
			t.Fatalf("partial evidence lost: %s %v", data, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(stage, "coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var coverage struct {
		Complete bool `json:"complete"`
	}
	if err := json.Unmarshal(data, &coverage); err != nil || !coverage.Complete {
		t.Fatalf("file-stage coverage semantics changed: %s %v", data, err)
	}
}

func TestScanYaraCancellationCLIKeepsPartialHits(t *testing.T) {
	binary := tidyshareTestBinary(t)
	entries, o := yaraFixture(t, `printf 'fixture %s\n' "$TIDYSHARE_YARA_TARGET"; echo owned-diagnostic >&2; : > "$TIDYSHARE_YARA_READY"; sleep 1`)
	scratch, ready := t.TempDir(), filepath.Join(t.TempDir(), "ready")
	var output bytes.Buffer
	command := exec.Command(binary, "scan", "-root", filepath.Dir(entries[0].Path), "-out", filepath.Join(t.TempDir(), "reports"), "-images=false", "-min-size", "1", "-settle", "0", "-workers", "1", "-yara-rules", o.yaraRules, "-yara-bin", o.yaraBin)
	command.Env = append(os.Environ(), "TMPDIR="+scratch, "TIDYSHARE_YARA_READY="+ready)
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(until) {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatal("YARA did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("cancelled scan returned success")
	}
	stage := partialScanStage(t, scratch)
	data, err := os.ReadFile(filepath.Join(stage, "yara.tsv"))
	if err != nil || !strings.Contains(string(data), "fixture") {
		t.Fatalf("cancelled hits lost: %s %v; %s", data, err, output.String())
	}
}

func TestMCPScanWriteFailureRetainsPrivateStage(t *testing.T) {
	binary := tidyshareTestBinary(t)
	root, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "owned.bin"), "owned", 48*time.Hour)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestReportWriterFailureHelper$")
	command.Env = append(os.Environ(), "TIDYSHARE_WRITER_CASE=mcp-exec", "TIDYSHARE_WRITER_APP="+binary, "TIDYSHARE_WRITER_ROOT="+out, "TIDYSHARE_WRITER_DATA="+root)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "owned-report-fault", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "scan", Arguments: map[string]any{"min_size": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("write failure reported success: %+v", result)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var operation mcpOperationResult
	if err := json.Unmarshal(data, &operation); err != nil {
		t.Fatal(err)
	}
	stage := partialScanStage(t, operation.ReportDir)
	if !strings.Contains(operation.Summary, stage) {
		t.Fatalf("MCP lost private failure path: %+v", operation)
	}
}
