package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReportFormats(t *testing.T) {
	for _, bad := range []string{"", "xml", "json,", "json,,md"} {
		if _, err := parseReportFormats(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	got, err := parseReportFormats(" json,md,jsonl,json ")
	if err != nil || !reflect.DeepEqual(got, []string{"json", "md", "jsonl"}) {
		t.Fatalf("formats=%v err=%v", got, err)
	}
}

func TestOpenPlainDirectoryDoesNotCreateReadOnlyPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "child")
	if dir, err := openPlainDirectory(path, false); err == nil {
		dir.Close()
		t.Fatal("missing read-only directory accepted")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("read-only open created a directory: %v", err)
	}
	dir, err := openPlainDirectory(path, true)
	if err != nil {
		t.Fatal(err)
	}
	dir.Close()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(path), alias); err != nil {
		t.Fatal(err)
	}
	if dir, err := openPlainDirectory(filepath.Join(alias, "child"), false); err == nil {
		dir.Close()
		t.Fatal("directory symlink accepted")
	}
}

func TestOperationReportsRecordsAndPermissions(t *testing.T) {
	out := filepath.Join(t.TempDir(), "reports")
	r := cleanupReport{
		SchemaVersion: 1, Operation: "duplicates", DryRun: true,
		Results: []cleanupResult{{Target: "/share/a|b\n.bin", Keeper: "/share/keep", Owner: "operator", Status: "would-delete", Bytes: 7}, {Target: "/share/c", Status: "skipped", Reason: "changed"}}, Skipped: 1, LogicalBytes: 7,
	}
	if err := writeOperationReports(out, []string{"json", "jsonl", "md"}, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "cleanup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded cleanupReport
	if err := json.Unmarshal(b, &decoded); err != nil || !reflect.DeepEqual(decoded, r) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	f, err := os.Open(filepath.Join(out, "cleanup.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var records []map[string]json.RawMessage
	for sc.Scan() {
		var record map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := sc.Err(); err != nil || len(records) != 3 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	if string(records[0]["type"]) != `"summary"` || string(records[1]["type"]) != `"result"` {
		t.Fatalf("record types=%v", records)
	}
	for i, want := range r.Results {
		b, err := json.Marshal(records[i+1])
		if err != nil {
			t.Fatal(err)
		}
		var got cleanupResult
		if err := json.Unmarshal(b, &got); err != nil || got != want {
			t.Fatalf("result=%+v want=%+v err=%v", got, want, err)
		}
	}
	md, err := os.ReadFile(filepath.Join(out, "cleanup.md"))
	if err != nil || !strings.Contains(string(md), "a\\|b\\n.bin") || !strings.Contains(string(md), "changed") || !strings.Contains(string(md), "operator") {
		t.Fatalf("markdown=%q err=%v", md, err)
	}
	for _, name := range []string{"cleanup.json", "cleanup.jsonl", "cleanup.md"} {
		st, err := os.Stat(filepath.Join(out, name))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s info=%v err=%v", name, st, err)
		}
	}
}

func TestReportPreflightRefusesUnsafeDestinations(t *testing.T) {
	for _, kind := range []string{"existing", "symlink", "hardlink", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			out := filepath.Join(base, "reports")
			if err := os.Mkdir(out, 0o700); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(base, "victim")
			if err := os.WriteFile(victim, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(out, "cleanup.md")
			var err error
			switch kind {
			case "existing":
				err = os.WriteFile(dst, []byte("preserve"), 0o600)
			case "symlink":
				err = os.Symlink(victim, dst)
			case "hardlink":
				err = os.Link(victim, dst)
			case "directory-symlink":
				err = os.Symlink(out, filepath.Join(base, "link"))
				out = filepath.Join(base, "link", "nested")
			}
			if err != nil {
				t.Fatal(err)
			}
			_, closeReports, err := prepareOperationReports(out, []string{"json", "md"})
			if closeReports != nil {
				closeReports()
			}
			if err == nil {
				t.Fatal("unsafe output accepted")
			}
			if kind != "directory-symlink" {
				if _, err := os.Lstat(filepath.Join(out, "cleanup.json")); !os.IsNotExist(err) {
					t.Fatalf("preflight reserved an earlier destination: %v", err)
				}
			}
			b, err := os.ReadFile(victim)
			if err != nil || string(b) != "preserve" {
				t.Fatalf("victim=%q err=%v", b, err)
			}
		})
	}
}

func TestScanSelectedFormats(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "file.txt"), "sample", 0)
	for _, format := range []string{"json", "jsonl", "md"} {
		t.Run(format, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "reports")
			if rc := runScan([]string{"-root", root, "-out", out, "-images=false", "-formats", format}); rc != 0 {
				t.Fatalf("scan rc=%d", rc)
			}
			for _, ext := range []string{"json", "jsonl", "md"} {
				_, err := os.Stat(filepath.Join(out, "report."+ext))
				if (err == nil) != (ext == format) {
					t.Fatalf("format=%s report.%s err=%v", format, ext, err)
				}
			}
			if _, err := os.Stat(filepath.Join(out, "dups.tsv")); err != nil {
				t.Fatal(err)
			}
			if format != "md" {
				if _, err := os.Stat(filepath.Join(out, "candidates.md")); !os.IsNotExist(err) {
					t.Fatalf("unexpected markdown err=%v", err)
				}
			}
			_, err := os.Stat(filepath.Join(out, "catalog.jsonl"))
			if (err == nil) != (format == "jsonl") {
				t.Fatalf("catalog for %s err=%v", format, err)
			}
		})
	}
}

func TestScanDefaultKeepsLegacyReports(t *testing.T) {
	out := t.TempDir()
	if rc := runScan([]string{"-root", t.TempDir(), "-out", out, "-images=false"}); rc != 0 {
		t.Fatalf("scan rc=%d", rc)
	}
	for _, name := range []string{"report.md", "catalog.jsonl", "dups.tsv", "stale.tsv", "candidates.md", "candidates.tsv"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("legacy report %s: %v", name, err)
		}
	}
}

func TestScanOutputRefusesExistingFiles(t *testing.T) {
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "dups.tsv"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rc := runScan([]string{"-root", t.TempDir(), "-out", out, "-images=false", "-formats", "json"}); rc == 0 {
		t.Fatal("scan overwrote an existing plan")
	}
	b, err := os.ReadFile(filepath.Join(out, "dups.tsv"))
	if err != nil || string(b) != "preserve" {
		t.Fatalf("plan=%q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(out, "report.json")); !os.IsNotExist(err) {
		t.Fatalf("output written before preflight: %v", err)
	}
}

func TestScanStructuredRecordsPreservePlanAndCatalog(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "same bytes", 0)
	write(t, filepath.Join(root, "b.txt"), "same bytes", 0)
	out := t.TempDir()
	if rc := runScan([]string{"-root", root, "-out", out, "-images=false", "-formats", "json,jsonl", "-min-size", "1", "-settle", "0"}); rc != 0 {
		t.Fatalf("scan rc=%d", rc)
	}
	b, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		SchemaVersion int          `json:"schema_version"`
		Root          string       `json:"root"`
		Records       []scanRecord `json:"records"`
	}
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.Root != root {
		t.Fatalf("metadata=%+v", report)
	}
	counts := map[string]int{}
	for _, record := range report.Records {
		counts[record.Type]++
		if record.Type == "dups" && record.Data["size"] != "10" {
			t.Fatalf("plan record=%+v", record)
		}
	}
	if counts["catalog"] != 2 || counts["dups"] != 2 {
		t.Fatalf("counts=%v", counts)
	}
	f, err := os.Open(filepath.Join(out, "report.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var summary scanRecord
	if err := dec.Decode(&summary); err != nil || summary.Type != "summary" {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if summary.Data["records"] != float64(len(report.Records)) {
		t.Fatalf("summary count=%v actual=%d", summary.Data["records"], len(report.Records))
	}
	for i, want := range report.Records {
		var got scanRecord
		if err := dec.Decode(&got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("record %d got=%+v want=%+v err=%v", i, got, want, err)
		}
	}
}

func TestCatalogReportPreservesLargeIntegers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.jsonl")
	if err := os.WriteFile(path, []byte("{\"path\":\"/share/large\",\"size\":9007199254740993}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var rows []scanRecord
	err := forEachScanRecord(path, "catalog", func(record scanRecord) error {
		rows = append(rows, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rows)
	if err != nil || !strings.Contains(string(b), `"size":9007199254740993`) {
		t.Fatalf("rows=%s err=%v", b, err)
	}
}

func TestScanCandidateKeeperControlCharacters(t *testing.T) {
	for character, escaped := range map[string]string{"\t": "\\t", "\n": "\\n", "|": "\\|"} {
		t.Run(fmt.Sprintf("%q", character), func(t *testing.T) {
			root, out := t.TempDir(), t.TempDir()
			keeper := filepath.Join(root, "a"+character+"keeper.txt")
			write(t, keeper, "same bytes", 48*time.Hour)
			write(t, filepath.Join(root, "z copy.txt"), "same bytes", 48*time.Hour)
			if rc := runScan([]string{"-root", root, "-out", out, "-images=false", "-min-size", "1", "-settle", "0"}); rc != 0 {
				t.Fatalf("default scan rc=%d", rc)
			}
			md, err := os.ReadFile(filepath.Join(out, "candidates.md"))
			if err != nil || !strings.Contains(string(md), root+"/a"+escaped+"keeper.txt") {
				t.Fatalf("escaped keeper missing from Markdown: %q err=%v", md, err)
			}
			body, err := os.ReadFile(filepath.Join(out, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Records []scanRecord `json:"records"`
			}
			if err := json.Unmarshal(body, &report); err != nil {
				t.Fatal(err)
			}
			for _, record := range report.Records {
				if record.Type == "candidates" && record.Data["kind"] == "dup-file" {
					if record.Data["note"] != "keep: "+keeper {
						t.Fatalf("note=%q keeper=%q", record.Data["note"], keeper)
					}
					return
				}
			}
			t.Fatal("duplicate candidate missing")
		})
	}
}

func BenchmarkPublishScanReports(b *testing.B) {
	stage := b.TempDir()
	f, err := os.Create(filepath.Join(stage, "catalog.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	w := bufio.NewWriter(f)
	for i := range 30000 {
		fmt.Fprintf(w, "{\"path\":\"/share/file-%06d\",\"area\":\"share\",\"category\":\"data\",\"mime\":\"text/plain\",\"size\":1024,\"mtime\":10,\"atime\":11,\"stale\":\"\"}\n", i)
	}
	if err := w.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	base := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if err := publishScanReports(stage, filepath.Join(base, fmt.Sprint(i)), []string{"json", "jsonl"}, "/share"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestScanRecordStreamingStopsBeforeInvalidTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.jsonl")
	if err := os.WriteFile(path, []byte("{\"path\":\"/share/first\"}\nnot-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("consumer stopped")
	var count int
	err := forEachScanRecord(path, "catalog", func(record scanRecord) error {
		count++
		if record.Data["path"] != "/share/first" {
			t.Fatalf("record=%+v", record)
		}
		return stop
	})
	if !errors.Is(err, stop) || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestScanRecordStreamingRetainsBoundedMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	for i := range 30000 {
		fmt.Fprintf(w, "{\"path\":\"/share/file-%06d\",\"category\":\"data\",\"size\":1024}\n", i)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	count := 0
	var peak uint64
	err = forEachScanRecord(path, "catalog", func(scanRecord) error {
		count++
		if count%5000 == 0 {
			runtime.GC()
			var current runtime.MemStats
			runtime.ReadMemStats(&current)
			if current.HeapAlloc > baseline.HeapAlloc {
				peak = max(peak, current.HeapAlloc-baseline.HeapAlloc)
			}
			if current.HeapAlloc > baseline.HeapAlloc+4<<20 {
				t.Fatalf("retained heap grew by %d bytes after %d records", current.HeapAlloc-baseline.HeapAlloc, count)
			}
		}
		return nil
	})
	if err != nil || count != 30000 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	t.Logf("records=%d peak retained heap growth=%d bytes", count, peak)
}
