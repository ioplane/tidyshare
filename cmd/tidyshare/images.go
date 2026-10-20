package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// localImage is one image from `podman images`; ID is the config digest (hex).
type localImage struct {
	ID, Owner  string
	Tags       []string
	Size       int64
	Created    int64
	Dangling   bool
	Containers int
}

// imgArchive is a docker-archive or oci-archive found on the share.
type imgArchive struct {
	Path, Format, Status string
	Size                 int64
	IDs, Tags, Stores    []string
}

type imageReport struct {
	Local    []localImage
	Archives []imgArchive
	SystemDF map[string]string
}

// first tar entry of anything `podman|docker save` writes
var (
	imgFirst = regexp.MustCompile(`^(manifest\.json|index\.json|oci-layout|repositories|blobs(/.*)?|[0-9a-f]{64}(\.json|\.tar|/.*)?)$`)
	hexJSON  = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)
)

func scanImages(es []entry, o opts) imageReport {
	r := imageReport{SystemDF: map[string]string{}}
	owners := append([]string{"root"}, splitList(o.imageUsers)...)
	for _, ow := range owners {
		imgs, df, err := listLocal(o.engine, ow)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: %s images (%s): %v\n", o.engine, ow, err)
			continue
		}
		r.Local = append(r.Local, imgs...)
		r.SystemDF[ow] = df
	}
	// archives: only tar/gzip by content, so renamed files are still found
	var cands []entry
	for _, e := range es {
		if e.Size >= 1<<20 && (e.Mime == "application/x-tar" || e.Mime == "application/gzip") {
			cands = append(cands, e)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	ch := make(chan entry)
	for i := 0; i < max(o.workers/2, 1); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range ch {
				a, err := readArchive(e.Path, e.Mime == "application/gzip")
				if err != nil {
					continue // not an image archive, or unreadable
				}
				a.Size = e.Size
				mu.Lock()
				r.Archives = append(r.Archives, a)
				mu.Unlock()
			}
		}()
	}
	for _, e := range cands {
		ch <- e
	}
	close(ch)
	wg.Wait()
	sort.Slice(r.Archives, func(i, j int) bool { return r.Archives[i].Size > r.Archives[j].Size })

	have := map[string][]string{}
	for _, l := range r.Local {
		have[l.ID] = append(have[l.ID], l.Owner)
	}
	for i := range r.Archives {
		a := &r.Archives[i]
		found := 0
		stores := map[string]bool{}
		for _, id := range a.IDs {
			if ow, ok := have[id]; ok {
				found++
				for _, s := range ow {
					stores[s] = true
				}
			}
		}
		switch {
		case len(a.IDs) > 0 && found == len(a.IDs):
			a.Status = "in-store"
		case found > 0:
			a.Status = "partial"
		default:
			a.Status = "absent"
		}
		for s := range stores {
			a.Stores = append(a.Stores, s)
		}
		sort.Strings(a.Stores)
	}
	return r
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// listLocal runs the engine as owner (root directly, others through runuser).
// Only podman's JSON shape is parsed; a `docker` binary works only when it is
// a podman shim.
func listLocal(engine, owner string) ([]localImage, string, error) {
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var c *exec.Cmd
		if owner == "root" {
			c = exec.CommandContext(ctx, engine, args...)
		} else {
			u, err := user.Lookup(owner)
			if err != nil {
				return nil, err
			}
			c = exec.CommandContext(ctx, "runuser", append([]string{
				"-u", owner, "--", "env",
				"HOME=" + u.HomeDir, "XDG_RUNTIME_DIR=/run/user/" + u.Uid, engine,
			}, args...)...)
			c.Dir = "/" // the caller's cwd may be unreadable for the target user
		}
		out, err := c.Output()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			err = fmt.Errorf("%w: %s", err, firstLine(ee.Stderr))
		}
		return out, err
	}
	out, err := run("images", "--all", "--no-trunc", "--format", "json")
	if err != nil {
		return nil, "", err
	}
	var raw []struct {
		ID         string   `json:"Id"`
		RepoTags   []string `json:"RepoTags"`
		Size       int64
		Created    int64
		Dangling   bool
		Containers int
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, "", err
	}
	var imgs []localImage
	for _, x := range raw {
		imgs = append(imgs, localImage{
			ID: strings.TrimPrefix(x.ID, "sha256:"), Owner: owner,
			Tags: x.RepoTags, Size: x.Size, Created: x.Created, Dangling: x.Dangling, Containers: x.Containers,
		})
	}
	df, _ := run("system", "df")
	return imgs, string(df), nil
}

// readArchive reads manifest.json / index.json from a docker-archive or
// oci-archive without extracting layers. Plain tar is skipped by seeking;
// gzip has to be streamed, so a non-image tar is rejected on its first entry.
func readArchive(path string, gz bool) (imgArchive, error) {
	f, err := os.Open(path)
	if err != nil {
		return imgArchive{}, err
	}
	defer f.Close()
	var src io.Reader = f
	if gz {
		zr, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
		if err != nil {
			return imgArchive{}, err
		}
		src = zr
	}
	tr := tar.NewReader(src)
	small := map[string][]byte{}
	for first := true; ; first = false {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return imgArchive{}, err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if first && !imgFirst.MatchString(name) {
			return imgArchive{}, errors.New("not an image archive")
		}
		if h.Size <= 1<<20 && (name == "manifest.json" || name == "index.json" ||
			strings.HasPrefix(name, "blobs/sha256/") || hexJSON.MatchString(name)) {
			b, err := io.ReadAll(tr)
			if err != nil {
				return imgArchive{}, err
			}
			small[name] = b
		}
	}
	a := imgArchive{Path: path}
	if b, ok := small["manifest.json"]; ok {
		var m []struct {
			Config   string
			RepoTags []string
		}
		if json.Unmarshal(b, &m) == nil && len(m) > 0 {
			a.Format = "docker-archive"
			for _, x := range m {
				id := strings.TrimSuffix(strings.TrimPrefix(x.Config, "blobs/sha256/"), ".json")
				a.IDs = append(a.IDs, id)
				a.Tags = append(a.Tags, x.RepoTags...)
			}
		}
	}
	if len(a.IDs) == 0 {
		if b, ok := small["index.json"]; ok {
			a.Format = "oci-archive"
			a.IDs, a.Tags = ociIDs(b, small, 0)
		}
	}
	if len(a.IDs) == 0 {
		return imgArchive{}, errors.New("no image config found")
	}
	return a, nil
}

// ociIDs resolves index -> manifest -> config digest (the image ID), following
// nested indexes (multi-arch) two levels deep.
func ociIDs(b []byte, blobs map[string][]byte, depth int) (ids, tags []string) {
	var doc struct {
		Config    struct{ Digest string }
		Manifests []struct {
			Digest      string
			Annotations map[string]string
		}
	}
	if depth > 2 || json.Unmarshal(b, &doc) != nil {
		return
	}
	if d := doc.Config.Digest; d != "" {
		ids = append(ids, strings.TrimPrefix(d, "sha256:"))
	}
	for _, m := range doc.Manifests {
		for _, k := range []string{"io.containerd.image.name", "org.opencontainers.image.ref.name"} {
			if t := m.Annotations[k]; t != "" {
				tags = append(tags, t)
			}
		}
		if blob, ok := blobs["blobs/sha256/"+strings.TrimPrefix(m.Digest, "sha256:")]; ok {
			i, t := ociIDs(blob, blobs, depth+1)
			ids, tags = append(ids, i...), append(tags, t...)
		}
	}
	return
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func writeImages(r imageReport, o opts) error {
	f, err := os.Create(filepath.Join(o.out, "images.tsv"))
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "kind\tstatus\tsize\tids\ttags\tstores\tpath")
	for _, a := range r.Archives {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", a.Format, a.Status, a.Size,
			strings.Join(shorts(a.IDs), ","), strings.Join(a.Tags, ","), strings.Join(a.Stores, ","), strconv.Quote(a.Path))
	}
	archived := map[string]bool{}
	for _, a := range r.Archives {
		for _, id := range a.IDs {
			archived[id] = true
		}
	}
	for _, l := range r.Local {
		st := "local-only"
		if archived[l.ID] {
			st = "archived"
		}
		kind := "local"
		if l.Dangling {
			kind = "local-dangling"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t\n", kind, st, l.Size, short(l.ID), strings.Join(l.Tags, ","), l.Owner)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	md, err := os.Create(filepath.Join(o.out, "images.md"))
	if err != nil {
		return err
	}
	defer md.Close()
	var dang, unused int64
	var nd, nu int
	for _, l := range r.Local {
		switch {
		case l.Dangling:
			dang += l.Size
			nd++
		case l.Containers == 0:
			unused += l.Size
			nu++
		}
	}
	fmt.Fprintf(md, "# Container images\n\n- local images: %d, dangling: %d (%s), tagged without containers: %d (%s; sizes include shared layers)\n- image archives on the share: %d\n\n",
		len(r.Local), nd, gib(dang), nu, gib(unused), len(r.Archives))
	for ow, df := range r.SystemDF {
		fmt.Fprintf(md, "## `%s system df` (%s)\n\n```text\n%s```\n\n", o.engine, ow, df)
	}
	fmt.Fprint(md, "## Image archives vs local storage\n\n| status | size | ids | tags | stores | path |\n|:---|---:|:---|:---|:---|:---|\n")
	for _, a := range r.Archives {
		fmt.Fprintf(md, "| %s | %s | %s | %s | %s | %s |\n", a.Status, gib(a.Size),
			strings.Join(shorts(a.IDs), " "), strings.Join(a.Tags, " "), strings.Join(a.Stores, " "), a.Path)
	}
	return nil
}

func shorts(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = short(id)
	}
	return out
}
