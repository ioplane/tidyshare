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
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// localImage is one image from the Podman API; ID is the config digest (hex).
type localImage struct {
	ID, Owner       string
	IdentityUnknown bool
	Tags            []string
	Size            int64
	Created         int64
	Dangling        bool
	Containers      int
}

// imgArchive is a docker-archive or oci-archive found on the share.
type imgArchive struct {
	Path, Format, Status string
	Size                 int64
	IDs, Tags, Stores    []string
}

type imageReport struct {
	Local       []localImage
	Archives    []imgArchive
	SystemDF    map[string]string
	Unavailable map[string]string
}

// first tar entry of anything `podman|docker save` writes
var (
	imgFirst = regexp.MustCompile(`^(manifest\.json|index\.json|oci-layout|repositories|blobs(/.*)?|[0-9a-f]{64}(\.json|\.tar|/.*)?)$`)
	hexJSON  = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)
)

func scanImages(es []entry, o opts) imageReport {
	r := imageReport{SystemDF: map[string]string{}, Unavailable: map[string]string{}}
	if o.engine == "docker" {
		store, err := resolveDockerStore(context.Background(), o.dockerContext)
		if err == nil {
			var images []localImage
			var summary string
			images, summary, err = store.inventory(context.Background())
			if err == nil {
				r.Local = images
				r.SystemDF["docker:"+store.Context] = summary
			}
		}
		if err != nil {
			r.Unavailable["docker"] = err.Error()
		}
	} else {
		owners := append([]string{"root"}, splitList(o.imageUsers)...)
		seen := map[string]bool{}
		for _, ow := range owners {
			if seen[ow] {
				continue
			}
			seen[ow] = true
			imgs, df, err := listLocal(o.imageSockets[ow], ow)
			if err != nil {
				r.Unavailable[ow] = err.Error()
				fmt.Fprintf(os.Stderr, "warn: Podman API images (%s): %v\n", ow, err)
				continue
			}
			r.Local = append(r.Local, imgs...)
			r.SystemDF[ow] = df
		}
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
		if l.IdentityUnknown {
			r.Unavailable["image-identity"] = "multi-platform config identities are not established"
			continue
		}
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
		case len(r.Unavailable) > 0:
			a.Status = "unknown"
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

func writeImages(r imageReport, o opts) (resultErr error) {
	f, err := os.Create(filepath.Join(o.out, "images.tsv"))
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "kind\tstatus\tsize\tids\ttags\tstores\tpath")
	owners := make([]string, 0, len(r.Unavailable))
	for owner := range r.Unavailable {
		owners = append(owners, owner)
	}
	slices.Sort(owners)
	for _, owner := range owners {
		fmt.Fprintf(w, "store\tunavailable\t0\t\t\t%s\t\n", strconv.Quote(owner))
	}
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
		} else if len(l.Tags) == 0 {
			kind = "local-untagged"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t\n", kind, st, l.Size, short(l.ID), strings.Join(l.Tags, ","), l.Owner)
	}
	if err := finishReport(w, f); err != nil {
		return err
	}

	md, err := os.Create(filepath.Join(o.out, "images.md"))
	if err != nil {
		return err
	}
	mw := bufio.NewWriter(md)
	defer func() { resultErr = errors.Join(resultErr, finishReport(mw, md)) }()
	var dang, unused int64
	var nd, nu, untagged int
	for _, l := range r.Local {
		if len(l.Tags) == 0 {
			untagged++
		}
		switch {
		case l.Dangling:
			dang += l.Size
			nd++
		case l.Containers == 0 && len(l.Tags) > 0:
			unused += l.Size
			nu++
		}
	}
	fmt.Fprintf(mw, "# Container images\n\n- local images: %d, dangling: %d (%s), tagged without containers: %d (%s; sizes include shared layers), untagged: %d\n- image archives on the share: %d\n\n",
		len(r.Local), nd, gib(dang), nu, gib(unused), untagged, len(r.Archives))
	for _, owner := range owners {
		fmt.Fprintf(mw, "- store %q unavailable: %s\n", owner, r.Unavailable[owner])
	}
	owners = owners[:0]
	for owner := range r.SystemDF {
		owners = append(owners, owner)
	}
	slices.Sort(owners)
	for _, ow := range owners {
		df := r.SystemDF[ow]
		fmt.Fprintf(mw, "## Container API disk usage (%s)\n\n```text\n%s```\n\n", ow, df)
	}
	fmt.Fprint(mw, "## Image archives vs local storage\n\n| status | size | ids | tags | stores | path |\n|:---|---:|:---|:---|:---|:---|\n")
	for _, a := range r.Archives {
		fmt.Fprintf(mw, "| %s | %s | %s | %s | %s | %s |\n", a.Status, gib(a.Size),
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
