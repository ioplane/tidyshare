package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func imageAPI(t *testing.T, payload string, rootless bool, diskUsage ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "podman.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("mutating method %s", r.Method)
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/libpod/_ping"):
			w.Header().Set("Libpod-API-Version", "5.8.2")
			fmt.Fprint(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/libpod/info"):
			fmt.Fprintf(w, `{"host":{"security":{"rootless":%v}},"store":{"graphRoot":"/fixture/storage","runRoot":"/fixture/runtime"}}`, rootless)
		case strings.HasSuffix(r.URL.Path, "/libpod/images/json"):
			if r.URL.Query().Get("all") != "true" {
				t.Error("intermediate images excluded")
			}
			fmt.Fprint(w, payload)
		case strings.HasSuffix(r.URL.Path, "/libpod/system/df"):
			if len(diskUsage) > 0 {
				fmt.Fprint(w, diskUsage[0])
				return
			}
			fmt.Fprint(w, `{"Images":[],"ImagesSize":17,"Containers":[],"Volumes":[]}`)
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := s.Serve(l); err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		<-done
	})
	return "unix://" + path
}

func TestPodmanSDKErrorsDoNotExposeResponseBodies(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := imageAPI(t, "[]", os.Geteuid() != 0, `{"ImagesSize":"bad","Containers":[{"Command":["password=fixture-private-value"]}]}`)
	o := opts{out: t.TempDir(), imageUsers: u.Username, imageSockets: map[string]string{u.Username: endpoint}, workers: 1}
	r := scanImages(nil, o)
	if message := r.Unavailable[u.Username]; message == "" || strings.Contains(message, "fixture-private-value") {
		t.Fatalf("unsafe error diagnostic: %s", message)
	}
	if err := writeImages(r, o); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(o.out, "images.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "fixture-private-value") {
		t.Fatal("response body leaked to report")
	}
}

func TestPodmanSDKUsesExplicitUnixSocket(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := imageAPI(t, `[{"Id":"`+strings.Repeat("ab", 32)+`","Names":["example.invalid/native:v1"],"Size":17,"Created":123,"Containers":0}]`, os.Geteuid() != 0)
	imgs, df, err := listLocal(endpoint, u.Username)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].ID != strings.Repeat("ab", 32) || imgs[0].Owner != u.Username || strings.Join(imgs[0].Tags, ",") != "example.invalid/native:v1" || imgs[0].Containers != 0 || !strings.Contains(df, "17") {
		t.Fatalf("wrong API inventory: %+v %q", imgs, df)
	}
}

func TestPodmanSDKRejectsIdentityMismatch(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := imageAPI(t, "[]", os.Geteuid() == 0)
	if _, _, err := listLocal(endpoint, u.Username); err == nil || !strings.Contains(err.Error(), "rootless identity") {
		t.Fatalf("wrong store identity accepted: %v", err)
	}
}

func TestPodmanSDKRejectsUnsafeSocket(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"symlink", "world writable", "wrong owner", "regular file", "missing"} {
		t.Run(kind, func(t *testing.T) {
			endpoint := imageAPI(t, "[]", os.Geteuid() != 0)
			path := strings.TrimPrefix(endpoint, "unix://")
			switch kind {
			case "symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				endpoint = "unix://" + alias
			case "world writable":
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			case "wrong owner":
				if os.Geteuid() != 0 {
					t.Skip("requires chown privilege")
				}
				if err := os.Chown(path, 65534, 65534); err != nil {
					t.Fatal(err)
				}
			case "regular file":
				p := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(p, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
				endpoint = "unix://" + p
			case "missing":
				endpoint = "unix://" + filepath.Join(t.TempDir(), "not-created")
			}
			if _, _, err := listLocal(endpoint, u.Username); err == nil {
				t.Fatal("unsafe endpoint accepted")
			}
		})
	}
}

func TestImageSocketFlagsRejectInvalidInputBeforeWrites(t *testing.T) {
	for _, value := range []string{"root=tcp://localhost:1234", "root=ssh://localhost/path", "root=unix://host/path", "root=unix:///tmp/a?x=1", "root=unix:///tmp/a#fragment", "root=unix:///tmp/../a", "other=unix:///tmp/a", "root=unix:///tmp/a,root=unix:///tmp/b", "root"} {
		t.Run(value, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			if rc := runScan([]string{"-root", t.TempDir(), "-out", out, "-image-sockets", value}); rc == 0 {
				t.Fatal("invalid endpoint configuration accepted")
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("invalid flags created output: %v", err)
			}
		})
	}
}
