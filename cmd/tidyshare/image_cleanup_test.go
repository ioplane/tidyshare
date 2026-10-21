package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type cleanupImageFixture struct {
	mu          sync.Mutex
	images      string
	containers  string
	graphRoot   string
	deletes     []string
	afterDelete func()
	removeReply string
}

func cleanupImageAPI(t *testing.T, f *cleanupImageFixture) (string, string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if f.graphRoot == "" {
		f.graphRoot = "/fixture/storage"
	}
	if f.containers == "" {
		f.containers = "[]"
	}
	path := filepath.Join(t.TempDir(), "api.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/libpod/_ping"):
			w.Header().Set("Libpod-API-Version", "6.1.4")
			fmt.Fprint(w, "OK")
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/libpod/info"):
			fmt.Fprintf(w, `{"host":{"security":{"rootless":%v}},"store":{"graphRoot":%q,"runRoot":"/fixture/runtime"}}`, os.Geteuid() != 0, f.graphRoot)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/libpod/images/json"):
			if r.URL.Query().Get("all") != "true" {
				t.Error("missing all images")
			}
			fmt.Fprint(w, f.images)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/libpod/containers/json"):
			if r.URL.Query().Get("all") != "true" || r.URL.Query().Get("external") != "true" {
				t.Error("external or stopped containers excluded")
			}
			fmt.Fprint(w, f.containers)
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/libpod/images/remove"):
			q := r.URL.Query()
			if q.Get("force") != "false" || q.Get("noprune") != "true" || q.Get("ignore") != "false" || len(q["images"]) != 1 || q.Get("all") == "true" {
				t.Errorf("unsafe delete params %v", q)
			}
			id := q.Get("images")
			f.deletes = append(f.deletes, id)
			if f.removeReply != "" {
				fmt.Fprint(w, f.removeReply)
			} else {
				fmt.Fprintf(w, `{"Deleted":[%q],"ExitCode":0}`, id)
			}
			if f.afterDelete != nil {
				f.afterDelete()
			} else {
				f.images = "[]"
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close(); <-done })
	return u.Username, "unix://" + path
}

func fixtureOrphan(id string) string {
	return fmt.Sprintf(`{"Id":%q,"ParentId":"","Names":[],"RepoTags":[],"Size":17,"Created":123,"Containers":0,"Dangling":true}`, id)
}

func TestImageCleanupDryRunExactRemovalAndRepeat(t *testing.T) {
	id := strings.Repeat("a", 64)
	f := &cleanupImageFixture{images: "[" + fixtureOrphan(id) + "]"}
	owner, endpoint := cleanupImageAPI(t, f)
	plan, err := planImages(context.Background(), owner, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ExactIDs) != 1 || plan.ExactIDs[0] != id || plan.GraphRoot != "/fixture/storage" {
		t.Fatalf("plan: %+v", plan)
	}
	report, err := cleanupImages(context.Background(), plan, false, 0)
	if err != nil || !report.DryRun || report.Changed != 0 || len(f.deletes) != 0 {
		t.Fatalf("dry run: %+v %v", report, err)
	}
	report, err = cleanupImages(context.Background(), plan, true, 1)
	if err != nil || report.Changed != 1 || report.LogicalBytes != 17 || len(f.deletes) != 1 || f.deletes[0] != id {
		t.Fatalf("execute: %+v %v", report, err)
	}
	report, err = cleanupImages(context.Background(), plan, true, 0)
	if err != nil || report.Changed != 0 || report.Skipped != 1 || len(f.deletes) != 1 {
		t.Fatalf("repeat: %+v %v", report, err)
	}
}

func TestImageCleanupRejectsUnsafeInventory(t *testing.T) {
	id := strings.Repeat("a", 64)
	for name, change := range map[string]func(*cleanupImageFixture){
		"unknown count": func(f *cleanupImageFixture) { f.images = strings.ReplaceAll(f.images, `,"Containers":0`, "") },
		"negative count": func(f *cleanupImageFixture) {
			f.images = strings.ReplaceAll(f.images, `"Containers":0`, `"Containers":-1`)
		},
		"named": func(f *cleanupImageFixture) {
			f.images = strings.ReplaceAll(f.images, `"Names":[]`, `"Names":["example.invalid/named:v1"]`)
		},
		"repo tag": func(f *cleanupImageFixture) {
			f.images = strings.ReplaceAll(f.images, `"RepoTags":[]`, `"RepoTags":["example.invalid/named:v1"]`)
		},
		"not dangling": func(f *cleanupImageFixture) {
			f.images = strings.ReplaceAll(f.images, `"Dangling":true`, `"Dangling":false`)
		},
		"container": func(f *cleanupImageFixture) {
			f.containers = fmt.Sprintf(`[{"Id":%q,"ImageID":%q}]`, strings.Repeat("c", 64), id)
		},
		"child": func(f *cleanupImageFixture) {
			f.images = "[" + fixtureOrphan(id) + "," + strings.Replace(fixtureOrphan(strings.Repeat("b", 64)), `"ParentId":""`, `"ParentId":"`+id+`"`, 1) + "]"
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &cleanupImageFixture{images: "[" + fixtureOrphan(id) + "]"}
			change(f)
			owner, endpoint := cleanupImageAPI(t, f)
			plan, err := planImages(context.Background(), owner, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range plan.ExactIDs {
				if got == id {
					t.Fatal("unsafe image planned")
				}
			}
		})
	}
}

func TestImageCleanupRechecksReferencesAndStore(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, kind := range []string{"container", "child", "count", "name", "store", "missing"} {
		t.Run(kind, func(t *testing.T) {
			f := &cleanupImageFixture{images: "[" + fixtureOrphan(id) + "]"}
			owner, endpoint := cleanupImageAPI(t, f)
			plan, err := planImages(context.Background(), owner, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			switch kind {
			case "container":
				f.containers = fmt.Sprintf(`[{"Id":%q,"ImageID":%q}]`, strings.Repeat("c", 64), id)
			case "child":
				f.images = "[" + fixtureOrphan(id) + "," + strings.Replace(fixtureOrphan(strings.Repeat("b", 64)), `"ParentId":""`, `"ParentId":"`+id+`"`, 1) + "]"
			case "count":
				f.images = strings.ReplaceAll(f.images, `"Containers":0`, `"Containers":1`)
			case "name":
				f.images = strings.ReplaceAll(f.images, `"Names":[]`, `"Names":["keep:v1"]`)
			case "store":
				f.graphRoot = "/different/storage"
			case "missing":
				f.images = "[]"
			}
			f.mu.Unlock()
			report, err := cleanupImages(context.Background(), plan, true, 0)
			if kind == "store" && err == nil {
				t.Fatal("store mismatch accepted")
			}
			if kind != "store" && err != nil {
				t.Fatal(err)
			}
			if report.Changed != 0 || len(f.deletes) != 0 {
				t.Fatalf("unsafe delete %+v", report)
			}
		})
	}
}

func TestImageCleanupRejectsWholeMalformedPlanBeforeDelete(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, kind := range []string{"version", "short ID", "duplicate", "candidate mismatch", "store path", "missing count", "missing candidates"} {
		t.Run(kind, func(t *testing.T) {
			f := &cleanupImageFixture{images: "[" + fixtureOrphan(id) + "]"}
			owner, endpoint := cleanupImageAPI(t, f)
			plan, err := planImages(context.Background(), owner, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "version":
				plan.SchemaVersion = 9
			case "short ID":
				plan.ExactIDs = append(plan.ExactIDs, "abc")
			case "duplicate":
				plan.ExactIDs = append(plan.ExactIDs, id)
			case "candidate mismatch":
				plan.Candidates[0].ID = strings.Repeat("b", 64)
			case "store path":
				plan.RunRoot = "../runtime"
			case "missing count":
				plan.Candidates[0].Containers = nil
			case "missing candidates":
				plan.ExactIDs = nil
				plan.Candidates = nil
			}
			if _, err := cleanupImages(context.Background(), plan, true, 0); err == nil {
				t.Fatal("malformed plan accepted")
			}
			if len(f.deletes) != 0 {
				t.Fatal("partial delete before validation")
			}
		})
	}
}

func TestImageCleanupCancellationAndMalformedResponses(t *testing.T) {
	id := strings.Repeat("a", 64)
	f := &cleanupImageFixture{images: "[" + fixtureOrphan(id) + "]"}
	owner, endpoint := cleanupImageAPI(t, f)
	plan, err := planImages(context.Background(), owner, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = cleanupImages(ctx, plan, true, 0); err == nil {
		t.Fatal("cancel ignored")
	}
	if len(f.deletes) != 0 {
		t.Fatal("delete after cancel")
	}
	for _, payload := range []string{"null", `[{"Id":"bad"}]`, `[{"Id":"` + id + `","ParentId":"","Containers":"secret-response-value"}]`} {
		f.mu.Lock()
		f.images = payload
		f.mu.Unlock()
		_, err = planImages(context.Background(), owner, endpoint)
		if err == nil || strings.Contains(err.Error(), "secret-response-value") {
			t.Fatalf("unsafe API error: %v", err)
		}
	}
}

func TestImageCleanupRejectsUnexpectedRemovalReport(t *testing.T) {
	id := strings.Repeat("a", 64)
	f := &cleanupImageFixture{images: "[" + fixtureOrphan(id) + "]", removeReply: `{"Deleted":["` + strings.Repeat("b", 64) + `"],"ExitCode":0}`}
	owner, endpoint := cleanupImageAPI(t, f)
	plan, err := planImages(context.Background(), owner, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cleanupImages(context.Background(), plan, true, 0)
	if err == nil {
		t.Fatal("unexpected removal identity accepted")
	}
}

func TestImageCleanupRechecksBeforeEveryRemoval(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	f := &cleanupImageFixture{images: "[" + fixtureOrphan(a) + "," + fixtureOrphan(b) + "]"}
	f.afterDelete = func() {
		f.images = "[" + fixtureOrphan(b) + "]"
		f.containers = fmt.Sprintf(`[{"Id":%q,"ImageID":%q}]`, strings.Repeat("c", 64), b)
	}
	owner, endpoint := cleanupImageAPI(t, f)
	plan, err := planImages(context.Background(), owner, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	report, err := cleanupImages(context.Background(), plan, true, 0)
	if err != nil || report.Changed != 1 || report.Skipped != 1 || len(f.deletes) != 1 || f.deletes[0] != a {
		t.Fatalf("stale references: %+v %v", report, err)
	}
}

func TestImageCleanupPlanJSON(t *testing.T) {
	f := &cleanupImageFixture{images: "[]"}
	owner, endpoint := cleanupImageAPI(t, f)
	plan, err := planImages(context.Background(), owner, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var got imageCleanupPlan
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if err = validateImageCleanupPlan(got); err != nil {
		t.Fatal(err)
	}
}

func TestImageCleanupRejectsUnknownReferences(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, kind := range []string{"missing parent", "null parent", "unknown container", "null containers"} {
		t.Run(kind, func(t *testing.T) {
			f := &cleanupImageFixture{images: "[" + fixtureOrphan(id) + "]"}
			switch kind {
			case "missing parent":
				f.images = strings.ReplaceAll(f.images, `"ParentId":"",`, "")
			case "null parent":
				f.images = strings.ReplaceAll(f.images, `"ParentId":""`, `"ParentId":null`)
			case "unknown container":
				f.containers = `[{"Id":"external-build-container"}]`
			case "null containers":
				f.containers = "null"
			}
			owner, endpoint := cleanupImageAPI(t, f)
			if _, err := planImages(context.Background(), owner, endpoint); err == nil {
				t.Fatal("unknown reference inventory accepted")
			}
			if len(f.deletes) != 0 {
				t.Fatal("planning mutated fixture")
			}
		})
	}
}

func TestImageCleanupNativeDanglingFieldPresence(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, kind := range []string{"true", "false", "omitted"} {
		t.Run(kind, func(t *testing.T) {
			payload := fixtureOrphan(id)
			if kind == "false" {
				payload = strings.ReplaceAll(payload, `"Dangling":true`, `"Dangling":false`)
			}
			if kind == "omitted" {
				payload = strings.ReplaceAll(payload, `,"Dangling":true`, "")
			}
			f := &cleanupImageFixture{images: "[" + payload + "]"}
			owner, endpoint := cleanupImageAPI(t, f)
			plan, err := planImages(context.Background(), owner, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if kind == "true" {
				want = 1
			}
			if len(plan.ExactIDs) != want {
				t.Fatalf("candidate count=%d want=%d", len(plan.ExactIDs), want)
			}
		})
	}
}
