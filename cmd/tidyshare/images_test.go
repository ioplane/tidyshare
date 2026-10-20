package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func imageEngine(t *testing.T, payload string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "engine")
	script := "#!/bin/sh\nif [ \"$1\" = images ]; then\ncat <<'JSON'\n" + payload + "\nJSON\nelse\nprintf 'fixture df\\n'\nfi\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRootlessRuntimeValidation(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())
	private := t.TempDir()
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeDir(private, uid); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := validateRuntimeDir(missing, uid); err == nil {
		t.Fatal("missing runtime accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("runtime created")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeDir(link, uid); err == nil {
		t.Fatal("symlink runtime accepted")
	}
	if err := os.Chmod(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeDir(private, uid); err == nil {
		t.Fatal("public runtime accepted")
	}
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeDir(private, strconv.Itoa(os.Getuid()+1)); err == nil {
		t.Fatal("wrong owner accepted")
	}
}

func TestLocalImageNames(t *testing.T) {
	engine := imageEngine(t, `[
 {"Id":"sha256:aa","Names":["example.invalid/native:v1"],"Containers":0},
 {"Id":"bb","RepoTags":["example.invalid/compat:v2"],"Containers":1},
 {"Id":"cc","Names":["<none>","<none>:<none>",""],"Dangling":false},
 {"Id":"dd","Names":["example.invalid/unknown:v3"]},
 {"Id":"ee","Names":["<none>"],"RepoTags":["example.invalid/fallback:v4"]}
 ]`)
	imgs, _, err := listLocal(engine, "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 5 || strings.Join(imgs[0].Tags, ",") != "example.invalid/native:v1" || strings.Join(imgs[1].Tags, ",") != "example.invalid/compat:v2" || strings.Join(imgs[4].Tags, ",") != "example.invalid/fallback:v4" {
		t.Fatalf("lost image names: %+v", imgs)
	}
	if len(imgs[2].Tags) != 0 || imgs[3].Containers != -1 {
		t.Fatalf("invented tags or container count: %+v", imgs)
	}
}

func TestUnprivilegedRootStoreIsUnavailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged container user")
	}
	if _, _, err := listLocal("must-not-be-executed", "root"); err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("root store was queried as another user: %v", err)
	}
}

func TestImageReportDoesNotCallUntaggedTagged(t *testing.T) {
	o := opts{out: t.TempDir(), engine: "podman"}
	r := imageReport{Local: []localImage{
		{ID: "aa", Tags: []string{"example.invalid/a:v1"}, Containers: 0},
		{ID: "bb", Containers: 0},
		{ID: "cc", Tags: []string{"example.invalid/c:v1"}, Containers: -1},
	}}
	if err := writeImages(r, o); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(o.out, "images.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "tagged without containers: 1") || !strings.Contains(string(b), "untagged: 1") {
		t.Fatalf("misleading counts: %s", b)
	}
}

func TestIncompleteStoresDoNotProveArchiveAbsent(t *testing.T) {
	id := strings.Repeat("ab", 32)
	archive := filepath.Join(t.TempDir(), "image.tar")
	tarOf(t, archive, [][2]string{{id + ".json", "{}"}, {"manifest.json", `[{"Config":"` + id + `.json"}]`}})
	o := opts{out: t.TempDir(), engine: imageEngine(t, "[]"), imageUsers: "tidyshare-no-such-test-account", workers: 2}
	r := scanImages([]entry{{Path: archive, Size: 1 << 20, Mime: "application/x-tar"}}, o)
	if len(r.Archives) != 1 || r.Archives[0].Status != "unknown" {
		t.Fatalf("failed store treated as absent: %+v", r.Archives)
	}
	if err := writeImages(r, o); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"images.tsv", "images.md"} {
		b, err := os.ReadFile(filepath.Join(o.out, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "unavailable") || !strings.Contains(string(b), o.imageUsers) {
			t.Fatalf("store failure missing in %s: %s", name, b)
		}
	}
	o.imageUsers = ""
	r = scanImages([]entry{{Path: archive, Size: 1 << 20, Mime: "application/x-tar"}}, o)
	if r.Archives[0].Status != "absent" {
		t.Fatalf("complete inventory did not prove absent: %+v", r.Archives)
	}
}
