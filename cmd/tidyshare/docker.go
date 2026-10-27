package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"
)

type dockerStore struct {
	Context    string
	Endpoint   string
	DaemonID   string
	socketInfo os.FileInfo
	mu         sync.Mutex
}

func newDockerStore(name, endpoint string) (*dockerStore, error) {
	path, err := socketPath(endpoint)
	if err != nil {
		return nil, errors.New("docker inventory requires a local Unix socket")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("docker socket is unavailable")
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o002 != 0 {
		return nil, errors.New("docker endpoint must be a local socket without world-write access")
	}
	return &dockerStore{Context: name, Endpoint: endpoint, socketInfo: info}, nil
}

func resolveDockerStore(ctx context.Context, name string) (*dockerStore, error) {
	if name == "" {
		name = os.Getenv("DOCKER_CONTEXT")
		if name == "" && os.Getenv("DOCKER_HOST") != "" {
			return newDockerStore("DOCKER_HOST", os.Getenv("DOCKER_HOST"))
		}
	}
	if strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\r\n") {
		return nil, errors.New("invalid docker context name")
	}
	args := []string{"context", "inspect"}
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "--format", "{{json .}}")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Context metadata is read without a shell; flags and executable are fixed.
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G702: the context name is one validated argument, not a command.
	var stdout, stderr mcpBoundedBuffer
	stdout.limit = 1 << 20
	stderr.limit = 64 << 10
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New("cannot resolve Docker context with the installed Docker CLI")
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, errors.New("docker context output exceeds safety limit")
	}
	var metadata struct {
		Name      string
		Endpoints map[string]struct{ Host string }
	}
	if err := json.Unmarshal(stdout.data, &metadata); err != nil {
		return nil, errors.New("invalid Docker context metadata")
	}
	return newDockerStore(metadata.Name, metadata.Endpoints["docker"].Host)
}

type dockerReadTransport struct {
	base  http.RoundTripper
	store *dockerStore
}

func (t dockerReadTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, errors.New("docker inventory permits GET and HEAD only")
	}
	path, err := socketPath(t.store.Endpoint)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !os.SameFile(t.store.socketInfo, info) {
		return nil, errors.New("docker socket identity changed")
	}
	response, err := t.base.RoundTrip(r)
	if err == nil {
		response.Body = http.MaxBytesReader(nil, response.Body, 32<<20)
	}
	return response, err
}

func (s *dockerStore) inventory(ctx context.Context) ([]localImage, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	path, _ := socketPath(s.Endpoint)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: dockerReadTransport{transport, s}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("docker inventory rejects redirects") }}
	c, err := client.New(client.WithHost(s.Endpoint), client.WithHTTPClient(hc))
	if err != nil {
		return nil, "", errors.New("cannot configure Docker client")
	}
	defer c.Close()
	info, err := c.Info(ctx, client.InfoOptions{})
	if err != nil || info.Info.ID == "" {
		return nil, "", errors.New("docker daemon identity unavailable")
	}
	s.mu.Lock()
	changed := s.DaemonID != "" && s.DaemonID != info.Info.ID
	if !changed {
		s.DaemonID = info.Info.ID
	}
	s.mu.Unlock()
	if changed {
		return nil, "", errors.New("docker daemon identity changed")
	}
	raw, err := c.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		return nil, "", errors.New("docker image inventory unavailable")
	}
	refs, err := c.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, "", errors.New("docker container references unavailable")
	}
	counts := map[string]int{}
	for _, x := range refs.Items {
		id := strings.TrimPrefix(x.ImageID, "sha256:")
		if !exactImageID(id) {
			return nil, "", errors.New("docker container reference has no exact image identity")
		}
		counts[id]++
	}
	var images []localImage
	for _, x := range raw.Items {
		id := strings.TrimPrefix(x.ID, "sha256:")
		if !exactImageID(id) || x.Size < 0 {
			return nil, "", errors.New("docker returned an invalid image identity or size")
		}
		var tags []string
		for _, tag := range x.RepoTags {
			if tag != "" && tag != "<none>:<none>" {
				tags = append(tags, tag)
			}
		}
		unknown := x.Descriptor != nil && (strings.Contains(x.Descriptor.MediaType, "index") || strings.Contains(x.Descriptor.MediaType, "manifest.list"))
		images = append(images, localImage{ID: id, Owner: "docker:" + s.Context, Tags: tags, Size: x.Size, Created: x.Created, Dangling: len(tags) == 0, Containers: counts[id], IdentityUnknown: unknown})
	}
	summary := fmt.Sprintf("context: %s\nendpoint: %s\ndaemon: %s\nimages: %d\ncontainers: %d\n", s.Context, s.Endpoint, info.Info.ID, len(images), len(refs.Items))
	df, err := c.DiskUsage(ctx, client.DiskUsageOptions{Images: true, Containers: true, Volumes: true, BuildCache: true})
	if err != nil {
		return images, summary + "disk usage: unavailable\n", nil
	}
	summary += fmt.Sprintf("image layer bytes: %d\nvolumes: %d; bytes: %d\nbuild cache: %d; bytes: %d\nSizes can overlap and do not predict host free space.\n", df.Images.TotalSize, df.Volumes.TotalCount, df.Volumes.TotalSize, df.BuildCache.TotalCount, df.BuildCache.TotalSize)
	return images, summary, nil
}

func runImages(args []string) int {
	f := flag.NewFlagSet("images", flag.ContinueOnError)
	engine := f.String("engine", "podman", "image inventory provider: podman or docker")
	dockerContext := f.String("docker-context", "", "local Docker context; empty uses Docker selection rules")
	out := f.String("out", "", "new report directory (required)")
	formatsValue := f.String("formats", "json,jsonl,md", "comma-separated report formats")
	owner := f.String("owner", "root", "Podman store owner")
	endpoint := f.String("socket", "", "owner-bound Podman Unix socket")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	if *out == "" || f.NArg() != 0 || (*engine != "podman" && *engine != "docker") {
		return fail(errors.New("images requires -out and a podman or docker engine"))
	}
	formats, err := parseReportFormats(*formatsValue)
	if err != nil {
		return fail(err)
	}
	r := imageReport{SystemDF: map[string]string{}, Unavailable: map[string]string{}}
	if *engine == "docker" {
		store, e := resolveDockerStore(context.Background(), *dockerContext)
		if e != nil {
			return fail(e)
		}
		images, summary, e := store.inventory(context.Background())
		if e != nil {
			return fail(e)
		}
		r.Local = images
		r.SystemDF["docker:"+store.Context] = summary
	} else {
		images, summary, e := listLocal(*endpoint, *owner)
		if e != nil {
			return fail(e)
		}
		r.Local = images
		r.SystemDF[*owner] = summary
	}
	if err := writeImageReports(r, *out, formats); err != nil {
		return fail(err)
	}
	return 0
}

func writeImageReports(r imageReport, out string, formats []string) error {
	stage, err := privateTemp("tidyshare-images-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := writeImages(r, opts{out: stage}); err != nil {
		return err
	}
	names := []string{"images.tsv"}
	for _, format := range formats {
		names = append(names, "images."+format)
	}
	files, closeFiles, err := reserveReports(out, names)
	if err != nil {
		return err
	}
	defer closeFiles()
	for name, file := range files {
		switch filepath.Ext(name) {
		case ".json":
			err = json.NewEncoder(file).Encode(r)
		case ".jsonl":
			err = json.NewEncoder(file).Encode(map[string]any{"type": "summary", "system_df": r.SystemDF})
			for _, image := range r.Local {
				if err == nil {
					err = json.NewEncoder(file).Encode(map[string]any{"type": "image", "data": image})
				}
			}
		default:
			var body []byte
			body, err = os.ReadFile(filepath.Join(stage, name))
			if err == nil {
				_, err = file.Write(body)
			}
		}
		if err != nil {
			return err
		}
		if err = file.Sync(); err != nil {
			return err
		}
	}
	return nil
}
