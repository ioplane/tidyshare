package main

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func write(t *testing.T, p, body string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestDupsAndApply(t *testing.T) {
	root, q := t.TempDir(), t.TempDir()
	day := 48 * time.Hour
	write(t, root+"/a/x.bin", "same-content", day)
	write(t, root+"/a/b/c/x copy.bin", "same-content", day)
	write(t, root+"/a/diff.bin", "same-conteNT", day)        // same size, different bytes
	write(t, root+"/a/new.bin", "same-content", time.Minute) // still being written
	if err := os.Link(root+"/a/x.bin", root+"/a/hard.bin"); err != nil {
		t.Fatal(err)
	}
	o := opts{root: root, out: t.TempDir(), minSize: 1, settle: time.Hour, staleDays: 365, workers: 2, depth: 2, skip: map[string]bool{}}
	es, err := walk(o)
	if err != nil {
		t.Fatal(err)
	}
	g := findDups(es, o)
	if len(g) != 1 || len(g[0]) != 2 {
		t.Fatalf("want 1 group of 2, got %v", g)
	}
	if err := writeReports(es, g, o); err != nil {
		t.Fatal(err)
	}
	// hard.bin and x.bin are one inode; either alias may represent it
	if got := g[0][0].Path; got != root+"/a/x.bin" && got != root+"/a/hard.bin" {
		t.Fatalf("keeper = %s", got)
	}
	// apply: quarantine must be on the same fs; TempDirs share /tmp
	if rc := runApply([]string{"-plan", o.out + "/dups.tsv", "-quarantine", q, "-yes"}); rc != 0 {
		t.Fatal("apply failed")
	}
	if _, err := os.Stat(root + "/a/b/c/x copy.bin"); !os.IsNotExist(err) {
		t.Fatal("dup not moved")
	}
	if _, err := os.Stat(g[0][0].Path); err != nil {
		t.Fatal("keeper lost")
	}
	if _, err := os.Stat(q + root + "/a/b/c/x copy.bin"); err != nil {
		t.Fatal("dup missing in quarantine")
	}
}

func TestStaleReason(t *testing.T) {
	cut := time.Now().Unix()
	for p, want := range map[string]string{"core.js": "", "core.1234": "junk", "a.tmp": "junk", "a.bin": ""} {
		if got := staleReason("/x/"+p, entry{Mtime: cut + 10}, cut); got != want {
			t.Errorf("%s: got %q want %q", p, got, want)
		}
	}
}

func TestMimeIgnoresExtension(t *testing.T) {
	p := t.TempDir() + "/data.bin"
	if err := os.WriteFile(p, []byte("\x7fELF\x02\x01\x01\x00"+string(make([]byte, 64))), 0o644); err != nil {
		t.Fatal(err)
	}
	es := []entry{{Path: p, Cat: "installer"}, {Path: p, Cat: "other"}}
	detectMime(es, 2)
	if es[0].Mime == "" || es[1].Cat != "binary" {
		t.Fatalf("got %+v", es)
	}
}

func tarOf(t *testing.T, p string, files [][2]string) {
	t.Helper()
	f, _ := os.Create(p)
	defer f.Close()
	tw := tar.NewWriter(f)
	for _, kv := range files {
		if err := tw.WriteHeader(&tar.Header{Name: kv[0], Size: int64(len(kv[1])), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(kv[1])); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
}

func TestReadArchive(t *testing.T) {
	id := strings.Repeat("ab", 32)
	mf := strings.Repeat("cd", 32)
	d := t.TempDir()
	tarOf(t, d+"/docker.tar", [][2]string{
		{id + ".json", "{}"},
		{"manifest.json", `[{"Config":"` + id + `.json","RepoTags":["a/b:1"]}]`},
	})
	a, err := readArchive(d+"/docker.tar", false)
	if err != nil || a.Format != "docker-archive" || a.IDs[0] != id || a.Tags[0] != "a/b:1" {
		t.Fatalf("docker: %+v %v", a, err)
	}
	tarOf(t, d+"/oci.tar", [][2]string{
		{"oci-layout", "{}"},
		{"index.json", `{"manifests":[{"digest":"sha256:` + mf + `","annotations":{"io.containerd.image.name":"x/y:2"}}]}`},
		{"blobs/sha256/" + mf, `{"config":{"digest":"sha256:` + id + `"}}`},
	})
	a, err = readArchive(d+"/oci.tar", false)
	if err != nil || a.Format != "oci-archive" || a.IDs[0] != id || a.Tags[0] != "x/y:2" {
		t.Fatalf("oci: %+v %v", a, err)
	}
	tarOf(t, d+"/plain.tar", [][2]string{{"README", "hi"}})
	if _, err := readArchive(d+"/plain.tar", false); err == nil {
		t.Fatal("plain tar accepted as image")
	}
}

func TestDedupe(t *testing.T) {
	d := t.TempDir()
	body := strings.Repeat("x", 3<<20)
	write(t, d+"/a", body, time.Hour)
	write(t, d+"/b", body, time.Hour)
	write(t, d+"/c", body[:len(body)-1]+"y", time.Hour)
	if err := dedupe(d+"/a", d+"/c", int64(len(body))); err == nil {
		t.Fatal("differing content accepted")
	} else if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
		t.Skip("filesystem has no dedupe support")
	}
	if err := dedupe(d+"/a", d+"/b", int64(len(body))); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(d + "/b"); string(b) != body {
		t.Fatal("content changed")
	}
}
