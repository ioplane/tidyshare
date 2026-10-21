package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.podman.io/podman/v6/pkg/bindings"
	"go.podman.io/podman/v6/pkg/bindings/containers"
	"go.podman.io/podman/v6/pkg/bindings/images"
	"go.podman.io/podman/v6/pkg/bindings/system"
	"go.podman.io/podman/v6/pkg/domain/entities/types"
)

type imageCleanupPlan struct {
	SchemaVersion int                     `json:"schema_version"`
	Owner         string                  `json:"owner"`
	Endpoint      string                  `json:"endpoint"`
	GraphRoot     string                  `json:"graphroot"`
	RunRoot       string                  `json:"runroot"`
	ExactIDs      []string                `json:"exact_ids"`
	Candidates    []imageCleanupCandidate `json:"candidates"`
}

type imageCleanupCandidate struct {
	ID         string `json:"id"`
	Size       int64  `json:"size"`
	Created    int64  `json:"created"`
	Dangling   bool   `json:"dangling"`
	Containers *int   `json:"containers"`
	Children   *int   `json:"children"`
}

func exactImageID(id string) bool {
	if len(id) != 64 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func validateImageCleanupPlan(p imageCleanupPlan) error {
	if p.SchemaVersion != 1 || p.Owner == "" || p.ExactIDs == nil || p.Candidates == nil || len(p.ExactIDs) != len(p.Candidates) {
		return errors.New("invalid image cleanup plan schema or candidate count")
	}
	if _, err := socketPath(p.Endpoint); err != nil {
		return err
	}
	for _, path := range []string{p.GraphRoot, p.RunRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return errors.New("image cleanup plan requires absolute clean store paths")
		}
	}
	seen := map[string]bool{}
	for i, id := range p.ExactIDs {
		c := p.Candidates[i]
		if !exactImageID(id) || seen[id] || c.ID != id || c.Size < 0 || !c.Dangling || c.Containers == nil || *c.Containers != 0 || c.Children == nil || *c.Children != 0 {
			return errors.New("image cleanup plan requires unique exact eligible image IDs and matching evidence")
		}
		seen[id] = true
	}
	return nil
}

type imageCleanupTransport struct {
	*http.Transport
	counts   map[string]int
	parents  map[string]bool
	deleteID string
}

func (t *imageCleanupTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		q := r.URL.Query()
		if r.Method != http.MethodDelete || !strings.HasSuffix(r.URL.Path, "/libpod/images/remove") || t.deleteID == "" || len(q["images"]) != 1 || q.Get("images") != t.deleteID || q.Get("force") != "false" || q.Get("noprune") != "true" || q.Get("ignore") != "false" || q.Get("all") != "false" {
			return nil, errors.New("image cleanup refuses an unreviewed mutation")
		}
	}
	response, err := t.Transport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	response.Body = http.MaxBytesReader(nil, response.Body, 32<<20)
	if response.StatusCode != http.StatusOK || r.Method != http.MethodGet {
		return response, nil
	}
	isImages := strings.HasSuffix(r.URL.Path, "/libpod/images/json")
	isContainers := strings.HasSuffix(r.URL.Path, "/libpod/containers/json")
	isInfo := strings.HasSuffix(r.URL.Path, "/libpod/info")
	if !isImages && !isContainers && !isInfo {
		return response, nil
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if isImages || isContainers {
		if len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '[' {
			return nil, errors.New("podman inventory must be an array")
		}
	}
	if isImages {
		// SDK scalar fields cannot distinguish omitted counts and parent identities.
		var presence []struct {
			ID         string `json:"Id"`
			Containers *int
			ParentID   *string `json:"ParentId"`
		}
		if err = json.Unmarshal(body, &presence); err != nil {
			return nil, errors.New("invalid Podman image inventory")
		}
		t.counts = map[string]int{}
		t.parents = map[string]bool{}
		for _, p := range presence {
			if _, ok := t.counts[p.ID]; ok {
				return nil, errors.New("duplicate Podman image identity")
			}
			t.counts[p.ID] = -1
			if p.Containers != nil {
				t.counts[p.ID] = *p.Containers
			}
			t.parents[p.ID] = p.ParentID != nil
		}
	}
	if isInfo {
		var presence struct {
			Host *struct{ Security *struct{ Rootless *bool } }
		}
		if err = json.Unmarshal(body, &presence); err != nil || presence.Host == nil || presence.Host.Security == nil || presence.Host.Security.Rootless == nil {
			return nil, errors.New("podman API returned incomplete rootless identity")
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func imageCleanupConnection(ctx context.Context, owner, endpoint string) (context.Context, *imageCleanupTransport, string, func(), error) {
	fail := func(err error) (context.Context, *imageCleanupTransport, string, func(), error) {
		return nil, nil, "", nil, err
	}
	endpoint, uid, err := podmanOwnerSocket(owner, endpoint)
	if err != nil {
		return fail(err)
	}
	conn, err := bindings.NewConnection(ctx, endpoint)
	if err != nil {
		return fail(&podmanAPIError{"connect", err})
	}
	client, err := bindings.GetClient(conn)
	if err != nil {
		return fail(&podmanAPIError{"connect", err})
	}
	transport, ok := client.Client.Transport.(*http.Transport)
	if !ok {
		return fail(errors.New("unexpected Podman SDK transport"))
	}
	t := &imageCleanupTransport{Transport: transport}
	client.Client.Transport = t
	client.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("podman cleanup rejects redirects") }
	closeConnection := func() { transport.CloseIdleConnections() }
	info, err := system.Info(conn, nil)
	if err != nil {
		closeConnection()
		return fail(&podmanAPIError{"store identity", err})
	}
	if info == nil || info.Host == nil || info.Store == nil || info.Host.Security.Rootless != (uid != 0) {
		closeConnection()
		return fail(errors.New("podman rootless identity does not match the requested owner"))
	}
	return conn, t, endpoint, closeConnection, nil
}

func imageCleanupStore(ctx context.Context) (string, string, error) {
	info, err := system.Info(ctx, nil)
	if err != nil {
		return "", "", &podmanAPIError{"store identity", err}
	}
	if info == nil || info.Store == nil {
		return "", "", errors.New("podman API returned incomplete store identity")
	}
	graph, run := info.Store.GraphRoot, info.Store.RunRoot
	for _, p := range []string{graph, run} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\x00\r\n") {
			return "", "", errors.New("podman API returned invalid store paths")
		}
	}
	return graph, run, nil
}

type imageCleanupSnapshot struct {
	images     map[string]*types.ImageSummary
	referenced map[string]bool
	children   map[string]bool
}

func imageCleanupInventory(ctx context.Context, t *imageCleanupTransport) (imageCleanupSnapshot, error) {
	out := imageCleanupSnapshot{images: map[string]*types.ImageSummary{}, referenced: map[string]bool{}, children: map[string]bool{}}
	raw, err := images.List(ctx, new(images.ListOptions).WithAll(true))
	if err != nil {
		return out, &podmanAPIError{"images", err}
	}
	for _, x := range raw {
		if x == nil {
			return out, errors.New("podman API returned an image without identity")
		}
		id := strings.TrimPrefix(x.ID, "sha256:")
		parent := strings.TrimPrefix(x.ParentId, "sha256:")
		if !exactImageID(id) || (parent != "" && !exactImageID(parent)) {
			return out, errors.New("podman API returned an invalid image identity")
		}
		if _, ok := out.images[id]; ok {
			return out, errors.New("podman API returned duplicate image identities")
		}
		out.images[id] = x
		if !t.parents[x.ID] {
			return out, errors.New("podman API omitted image parent evidence")
		}
		if parent != "" {
			out.children[parent] = true
		}
	}
	refs, err := containers.List(ctx, new(containers.ListOptions).WithAll(true).WithExternal(true))
	if err != nil {
		return out, &podmanAPIError{"container references", err}
	}
	for _, c := range refs {
		id := strings.TrimPrefix(c.ImageID, "sha256:")
		if !exactImageID(id) {
			return out, errors.New("podman API returned a container without an exact image identity")
		}
		out.referenced[id] = true
	}
	return out, nil
}

func (s imageCleanupSnapshot) eligible(id string, t *imageCleanupTransport) bool {
	x := s.images[id]
	if x == nil || !x.Dangling || x.ReadOnly || x.Size < 0 || t.counts[x.ID] != 0 || s.referenced[id] || s.children[id] {
		return false
	}
	for _, names := range [][]string{x.Names, x.RepoTags} {
		for _, name := range names {
			if name != "" && name != "<none>" && name != "<none>:<none>" {
				return false
			}
		}
	}
	return true
}

func planImages(ctx context.Context, owner, endpoint string) (imageCleanupPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	conn, t, endpoint, closeConnection, err := imageCleanupConnection(ctx, owner, endpoint)
	if err != nil {
		return imageCleanupPlan{}, err
	}
	defer closeConnection()
	graph, run, err := imageCleanupStore(conn)
	if err != nil {
		return imageCleanupPlan{}, err
	}
	snapshot, err := imageCleanupInventory(conn, t)
	if err != nil {
		return imageCleanupPlan{}, err
	}
	p := imageCleanupPlan{SchemaVersion: 1, Owner: owner, Endpoint: endpoint, GraphRoot: graph, RunRoot: run, ExactIDs: []string{}, Candidates: []imageCleanupCandidate{}}
	for id := range snapshot.images {
		if snapshot.eligible(id, t) {
			p.ExactIDs = append(p.ExactIDs, id)
		}
	}
	slices.Sort(p.ExactIDs)
	for _, id := range p.ExactIDs {
		x := snapshot.images[id]
		zero := 0
		p.Candidates = append(p.Candidates, imageCleanupCandidate{ID: id, Size: x.Size, Created: x.Created, Dangling: true, Containers: &zero, Children: &zero})
	}
	return p, validateImageCleanupPlan(p)
}

func cleanupImages(ctx context.Context, p imageCleanupPlan, execute bool, limit int) (cleanupReport, error) {
	report := cleanupReport{SchemaVersion: 1, Operation: "images", DryRun: !execute, Results: []cleanupResult{}}
	if err := validateImageCleanupPlan(p); err != nil {
		return report, err
	}
	if limit < 0 {
		return report, errors.New("image cleanup limit must not be negative")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	conn, t, _, closeConnection, err := imageCleanupConnection(ctx, p.Owner, p.Endpoint)
	if err != nil {
		return report, err
	}
	defer closeConnection()
	for i, id := range p.ExactIDs {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		result := cleanupResult{Target: id, Owner: p.Owner, Status: "skipped"}
		if limit > 0 && i >= limit {
			result.Reason = "limit"
			report.Results = append(report.Results, result)
			report.Skipped++
			continue
		}
		graph, run, err := imageCleanupStore(conn)
		if err != nil {
			return report, err
		}
		if graph != p.GraphRoot || run != p.RunRoot {
			return report, errors.New("podman store identity changed since the reviewed plan")
		}
		snapshot, err := imageCleanupInventory(conn, t)
		if err != nil {
			return report, err
		}
		switch {
		case snapshot.images[id] == nil:
			result.Reason = "missing"
		case !snapshot.eligible(id, t):
			result.Reason = "image gained a name, reference, child, or unknown eligibility"
		case !execute:
			result.Status = "would-remove"
			result.Bytes = snapshot.images[id].Size
		default:
			t.deleteID = id
			deleted, failures := images.Remove(conn, []string{id}, new(images.RemoveOptions).WithAll(false).WithForce(false).WithNoPrune(true).WithIgnore(false))
			t.deleteID = ""
			if len(failures) > 0 {
				result.Status = "error"
				result.Reason = "podman removal failed"
				report.Results = append(report.Results, result)
				return report, &podmanAPIError{"image removal", errors.Join(failures...)}
			}
			if deleted == nil || deleted.ExitCode != 0 || len(deleted.Deleted) != 1 || strings.TrimPrefix(deleted.Deleted[0], "sha256:") != id || len(deleted.Untagged) != 0 {
				result.Status = "error"
				result.Reason = "unexpected removal response"
				report.Results = append(report.Results, result)
				return report, errors.New("podman image removal returned an unexpected result; stop and inspect the store")
			}
			result.Status = "removed"
			result.Bytes = snapshot.images[id].Size
			report.Changed++
			report.LogicalBytes += result.Bytes
		}
		if result.Status == "skipped" {
			report.Skipped++
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}
