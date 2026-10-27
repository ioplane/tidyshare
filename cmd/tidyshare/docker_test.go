package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerInventoryRejectsRemoteEndpoint(t *testing.T) {
	if _, err := newDockerStore("fixture", "tcp://127.0.0.1:2375"); err == nil {
		t.Fatal("remote endpoint accepted")
	}
}

func TestDockerInventoryAndSocketReplacement(t *testing.T) {
	id := strings.Repeat("a", 64)
	root := t.TempDir()
	socket := filepath.Join(root, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Errorf("mutation: %s", r.Method)
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.55")
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/info"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ID": "fixture-daemon", "Driver": "overlay2", "DockerRootDir": "/fixture/data"})
		case strings.HasSuffix(r.URL.Path, "/images/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "sha256:" + id, "RepoTags": []string{"fixture:v1"}, "Size": 512, "Containers": -1}})
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": strings.Repeat("b", 64), "ImageID": "sha256:" + id, "Command": "secret-command-must-not-be-reported"}})
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			_, _ = w.Write([]byte(`{"LayersSize":512,"Images":[],"Containers":[],"Volumes":[],"BuildCache":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	store, err := newDockerStore("fixture", "unix://"+socket)
	if err != nil {
		t.Fatal(err)
	}
	images, summary, err := store.inventory(context.Background())
	if err != nil || len(images) != 1 || images[0].ID != id || images[0].Containers != 1 {
		t.Fatalf("inventory: %+v %s %v", images, summary, err)
	}
	if strings.Contains(summary, "secret-command") {
		t.Fatal("container command leaked")
	}
	source := workstationRuntimeSource{Engine: "docker", Docker: store}
	observation, err := source.observe(context.Background())
	if err != nil || observation.State != "complete" || observation.Identity != "fixture-daemon" {
		t.Fatalf("workstation runtime: %+v %v", observation, err)
	}
	plan, err := createManagedPlan(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"docker_build"}, Docker: store})
	if err != nil || len(plan.Targets) != 1 || plan.Targets[0].State != "blocked" || plan.Targets[0].DaemonID != "fixture-daemon" {
		t.Fatalf("managed Docker identity %+v %v", plan, err)
	}
	if err := os.Rename(socket, socket+".old"); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, _, err := store.inventory(context.Background()); err == nil {
		t.Fatal("socket replacement accepted")
	}
	if observation, err := source.observe(context.Background()); err == nil || observation.State != "partial" {
		t.Fatal("workstation runtime accepted replaced socket")
	}
}
