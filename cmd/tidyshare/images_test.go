package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalImageNames(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := imageAPI(t, `[
 {"Id":"sha256:aa","Names":["example.invalid/native:v1"],"Containers":0},
 {"Id":"bb","RepoTags":["example.invalid/compat:v2"],"Containers":1},
 {"Id":"cc","Names":["<none>","<none>:<none>",""],"Dangling":false},
 {"Id":"dd","Names":["example.invalid/unknown:v3"]},
 {"Id":"ee","Names":["<none>"],"RepoTags":["example.invalid/fallback:v4"]}
 ]`, os.Geteuid() != 0)
	imgs, _, err := listLocal(endpoint, u.Username)
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
	o := opts{out: t.TempDir()}
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
	if os.Geteuid() != 0 {
		t.Skip("root inventory requires root")
	}
	id := strings.Repeat("ab", 32)
	archive := filepath.Join(t.TempDir(), "image.tar")
	tarOf(t, archive, [][2]string{{id + ".json", "{}"}, {"manifest.json", `[{"Config":"` + id + `.json"}]`}})
	o := opts{out: t.TempDir(), imageSockets: map[string]string{"root": imageAPI(t, "[]", false)}, imageUsers: "tidyshare-no-such-test-account", workers: 2}
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
