package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestScanRejectsInvalidOptionsBeforeWrites(t *testing.T) {
	for _, args := range [][]string{{"-workers", "0"}, {"-workers", "-1"}, {"-min-size", "0"}, {"-settle", "-1s"}, {"-stale-days", "-1"}, {"-depth", "-1"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "reports")
			all := append([]string{"-root", t.TempDir(), "-out", out, "-images=false"}, args...)
			if rc := runScan(all); rc == 0 {
				t.Fatal("invalid scan accepted")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("invalid scan wrote reports")
			}
		})
	}
}

func TestRelativeScanEmitsAbsolutePlan(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	write(t, filepath.Join(root, "a.zip"), "same content", time.Hour)
	write(t, filepath.Join(root, "b.zip"), "same content", time.Hour)
	t.Chdir(root)
	if rc := runScan([]string{"-root", ".", "-out", out, "-images=false", "-min-size", "1", "-workers", "1", "-settle", "0"}); rc != 0 {
		t.Fatal("relative scan failed")
	}
	b, err := os.ReadFile(filepath.Join(out, "dups.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(rows) != 3 {
		t.Fatalf("duplicate fixture not scanned: %s", b)
	}
	for _, r := range rows[1:] {
		c := strings.Split(r, "\t")
		p, err := strconv.Unquote(c[4])
		if err != nil || !filepath.IsAbs(p) {
			t.Fatalf("relative path in plan: %q, %v", p, err)
		}
	}
}

func TestScanRejectsInvalidRootBeforeWrites(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	write(t, file, "data", time.Hour)
	link := filepath.Join(base, "link")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(base, "missing"), file, link} {
		out := filepath.Join(t.TempDir(), "reports")
		if rc := runScan([]string{"-root", root, "-out", out, "-images=false"}); rc == 0 {
			t.Fatalf("invalid root accepted: %s", root)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("invalid root wrote reports")
		}
	}
}

func TestFilesystemRootDirectorySummary(t *testing.T) {
	o := opts{root: "/", out: t.TempDir(), depth: 2, minSize: 1, staleDays: 365}
	es := []entry{{Path: "/fixture/nested/file.zip", Area: "fixture", Size: 10, Cat: "archive", Mime: "application/zip", Mtime: time.Now().Unix()}}
	if err := writeSummary(es, o, 0, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(o.out, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "| fixture/nested |") || strings.Contains(string(b), "|  |") {
		t.Fatalf("invalid filesystem-root directories: %s", b)
	}
}
