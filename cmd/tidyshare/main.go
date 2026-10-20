// tidyshare scans a large file share read-only: it categorises files by
// extension and content, finds byte-identical duplicates, flags stale files,
// matches container image archives against local storage and ranks deletion
// candidates. `apply` acts on a reviewed duplicate plan: it either moves the
// duplicates into a same-filesystem quarantine or shares their extents with the
// kept copy; nothing is deleted.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/zeebo/xxh3"
	"golang.org/x/sys/unix"
)

const sampleSize = 64 << 10 // head+tail bytes hashed in the cheap first pass

var catByExt = map[string]string{}

func init() {
	for cat, exts := range map[string]string{
		"iso-image":  ".iso .img .vhd .vhdx .wim .dmg",
		"vm-disk":    ".qcow2 .vmdk .ova .ovf .raw .vdi .nvram",
		"archive":    ".zip .tar .gz .tgz .bz2 .xz .zst .7z .rar .tbz2 .txz .cab",
		"package":    ".rpm .deb .apk .whl .jar .war .nupkg .pkg .vib .bundle",
		"installer":  ".exe .msi .run .bin .sh .appimage",
		"firmware":   ".swi .swix .pkgx .spkg .fw .cap .upx .ipk .bundle2",
		"document":   ".pdf .doc .docx .xls .xlsx .ppt .pptx .odt .md .txt .rtf .html .htm .epub .chm",
		"data":       ".json .jsonl .csv .tsv .xml .yaml .yml .sql .db .sqlite .parquet .pcap .pcapng .cap2",
		"log":        ".log .out .err",
		"source":     ".go .rs .py .c .h .cpp .hpp .java .js .ts .rb .pl .lua .tf .proto",
		"media":      ".png .jpg .jpeg .gif .svg .webp .mp4 .mkv .avi .mov .mp3 .wav",
		"dump":       ".dmp .core .hprof .mdmp",
		"cert-key":   ".pem .crt .cer .key .p12 .pfx .jks",
		"disk-cache": ".tmp .part .crdownload .swp .bak .old .orig",
	} {
		for _, e := range strings.Fields(exts) {
			catByExt[e] = cat
		}
	}
}

type entry struct {
	Path, Area, Cat string
	Size            int64
	Mtime, Atime    int64
	Dev, Ino        uint64
	Mime            string // detected from content, not from the extension
	Stale           string // "", "junk", "cold"
}

type opts struct {
	root, out          string
	minSize            int64
	settle             time.Duration
	staleDays          int
	workers, depth     int
	skip               map[string]bool
	engine, imageUsers string
	images             bool
	yaraRules, yaraBin string
	yaraCats           string
	yaraMax            int64
}

// version, commit are set at link time by the release build.
var (
	version = "dev"
	commit  = "none"
)

const usage = `usage: tidyshare <command> [flags]

commands:
  scan      read-only scan; writes reports to -out
  apply     act on a reviewed dups.tsv (dry run unless -yes)
  version   print version

run "tidyshare <command> -h" for the flags of a command.
`

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "scan":
		os.Exit(runScan(os.Args[2:]))
	case "apply":
		os.Exit(runApply(os.Args[2:]))
	case "version", "-version", "--version":
		fmt.Printf("tidyshare %s (%s)\n", version, commit)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func runScan(args []string) int {
	var o opts
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	skip := fs.String("skip", "", "comma-separated absolute dirs to skip")
	fs.StringVar(&o.root, "root", "", "tree to scan (required)")
	fs.StringVar(&o.out, "out", "tidyshare-report", "report directory; keep it off a nearly full share")
	fs.Int64Var(&o.minSize, "min-size", 1<<20, "ignore files smaller than this for dedup")
	fs.DurationVar(&o.settle, "settle", time.Hour, "skip files modified more recently (still being written)")
	fs.IntVar(&o.staleDays, "stale-days", 180, "mtime older than this = cold")
	fs.IntVar(&o.workers, "workers", 8, "parallel hashers")
	fs.IntVar(&o.depth, "depth", 3, "directory summary depth under root")
	fs.BoolVar(&o.images, "images", true, "scan container images and match image archives")
	fs.StringVar(&o.engine, "engine", "podman", "container engine binary; only podman JSON output is parsed")
	fs.StringVar(&o.imageUsers, "image-users", "", "comma-separated rootless users whose storage is also listed")
	fs.StringVar(&o.yaraRules, "yara-rules", "", "YARA rules file or directory (empty = no YARA scan)")
	fs.StringVar(&o.yaraBin, "yara-bin", "yara", "classic yara CLI (needs --scan-list)")
	fs.StringVar(&o.yaraCats, "yara-cats", "binary,installer,package,firmware,dump", "categories to scan with YARA")
	fs.Int64Var(&o.yaraMax, "yara-max-size", 512<<20, "skip larger files in the YARA scan")
	_ = fs.Parse(args) // flag.ExitOnError exits on a parse error
	if o.root == "" {
		return fail(errors.New("-root is required"))
	}
	o.root = filepath.Clean(o.root)
	o.skip = map[string]bool{}
	for _, s := range strings.Split(*skip, ",") {
		if s != "" {
			o.skip[filepath.Clean(s)] = true
		}
	}
	if abs, err := filepath.Abs(o.out); err == nil {
		o.skip[abs] = true
	}
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return fail(err)
	}
	es, err := walk(o)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "scanned %d files\n", len(es))
	detectMime(es, o.workers)
	groups := findDups(es, o)
	if err := writeReports(es, groups, o); err != nil {
		return fail(err)
	}
	var ir imageReport
	if o.images {
		ir = scanImages(es, o)
		if err := writeImages(ir, o); err != nil {
			return fail(err)
		}
	}
	if _, err := runYara(es, o); err != nil {
		fmt.Fprintln(os.Stderr, "warn: yara:", err)
	}
	if err := writeCandidates(es, groups, ir, o); err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "reports in %s\n", o.out)
	return 0
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

// walk is single-threaded: lstat of ~1.5M inodes is bounded by the page cache,
// not by CPU. ponytail: parallelise per top-level dir if a scan exceeds ~10 min.
func walk(o opts) ([]entry, error) {
	cutoff := time.Now().AddDate(0, 0, -o.staleDays).Unix()
	var es []entry
	err := filepath.WalkDir(o.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Fprintln(os.Stderr, "warn:", err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if o.skip[p] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st := info.Sys().(*syscall.Stat_t)
		rel, _ := filepath.Rel(o.root, p)
		area, _, _ := strings.Cut(rel, string(filepath.Separator))
		e := entry{
			Path: p, Area: area, Size: info.Size(), Mtime: info.ModTime().Unix(),
			Atime: st.Atim.Sec, Dev: uint64(st.Dev), Ino: st.Ino,
		}
		e.Cat = categorize(p)
		e.Stale = staleReason(p, e, cutoff)
		es = append(es, e)
		return nil
	})
	return es, err
}

// detectMime reads only the first 3 KiB of each file (library default limit).
func detectMime(es []entry, workers int) {
	idx := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				m, err := mimetype.DetectFile(es[i].Path)
				if err != nil {
					es[i].Mime = "unreadable"
					continue
				}
				es[i].Mime, _, _ = strings.Cut(m.String(), ";")
				if es[i].Cat == "other" {
					es[i].Cat = catByMime(es[i].Mime)
				}
			}
		}()
	}
	for i := range es {
		idx <- i
	}
	close(idx)
	wg.Wait()
}

// catByMime refines files whose extension told us nothing.
func catByMime(m string) string {
	switch {
	case strings.HasPrefix(m, "text/"):
		return "text"
	case strings.HasPrefix(m, "image/"), strings.HasPrefix(m, "video/"), strings.HasPrefix(m, "audio/"):
		return "media"
	case strings.Contains(m, "executable"), strings.Contains(m, "sharedlib"), strings.Contains(m, "elf"),
		strings.Contains(m, "mach"), strings.Contains(m, "msdownload"):
		return "binary"
	case strings.Contains(m, "zip"), strings.Contains(m, "tar"), strings.Contains(m, "gzip"),
		strings.Contains(m, "zstd"), strings.Contains(m, "xz"), strings.Contains(m, "7z"):
		return "archive"
	case strings.Contains(m, "json"), strings.Contains(m, "xml"), strings.Contains(m, "sqlite"):
		return "data"
	}
	return "other"
}

func categorize(p string) string {
	if c, ok := catByExt[strings.ToLower(filepath.Ext(p))]; ok {
		return c
	}
	return "other"
}

func staleReason(p string, e entry, cutoff int64) string {
	b := filepath.Base(p)
	switch {
	case strings.HasSuffix(b, "~"), isCoreDump(b), strings.HasPrefix(b, ".~lock"),
		catByExt[strings.ToLower(filepath.Ext(b))] == "disk-cache":
		return "junk"
	case e.Mtime < cutoff: // atime is not used: any prior full read (backup, hashing) resets it
		return "cold"
	}
	return ""
}

func isCoreDump(b string) bool {
	n, ok := strings.CutPrefix(b, "core.")
	if !ok {
		return false
	}
	_, err := strconv.Atoi(n)
	return err == nil
}

// findDups narrows candidates by size, then head+tail hash, then full hash.
func findDups(es []entry, o opts) [][]entry {
	newest := time.Now().Add(-o.settle).Unix()
	seen := map[[2]uint64]bool{} // hardlinks are one file, not duplicates
	bySize := map[int64][]entry{}
	for _, e := range es {
		k := [2]uint64{e.Dev, e.Ino}
		if e.Size < o.minSize || e.Mtime > newest || seen[k] || strings.Contains(e.Path, "/.git/") {
			continue
		}
		seen[k] = true
		bySize[e.Size] = append(bySize[e.Size], e)
	}
	var groups [][]entry
	for _, g := range bySize {
		if len(g) > 1 {
			groups = append(groups, g)
		}
	}
	for _, h := range []func(string, int64) ([16]byte, error){sampleHash, fullHash} {
		groups = regroup(groups, h, o.workers)
	}
	return groups
}

func regroup(groups [][]entry, h func(string, int64) ([16]byte, error), workers int) [][]entry {
	type job struct{ gi, ei int }
	sums := make([][][16]byte, len(groups))
	jobs := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for gi, g := range groups {
		sums[gi] = make([][16]byte, len(g))
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				e := groups[j.gi][j.ei]
				s, err := h(e.Path, e.Size)
				if err != nil {
					fmt.Fprintln(os.Stderr, "warn: hash:", err)
					continue // unreadable file is dropped from dedup, never deleted
				}
				mu.Lock()
				sums[j.gi][j.ei] = s
				mu.Unlock()
			}
		}()
	}
	for gi, g := range groups {
		for ei := range g {
			jobs <- job{gi, ei}
		}
	}
	close(jobs)
	wg.Wait()
	var out [][]entry
	for gi, g := range groups {
		m := map[[16]byte][]entry{}
		for ei, e := range g {
			if s := sums[gi][ei]; s != ([16]byte{}) {
				m[s] = append(m[s], e)
			}
		}
		for _, sg := range m {
			if len(sg) > 1 {
				out = append(out, sg)
			}
		}
	}
	return out
}

func sum(h *xxh3.Hasher128) [16]byte {
	u := h.Sum128()
	var b [16]byte
	for i := 0; i < 8; i++ {
		b[i], b[8+i] = byte(u.Hi>>(56-8*i)), byte(u.Lo>>(56-8*i))
	}
	return b
}

func sampleHash(p string, size int64) ([16]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return [16]byte{}, err
	}
	defer f.Close()
	h := xxh3.New128()
	buf := make([]byte, sampleSize)
	for _, off := range []int64{0, max(size-sampleSize, 0)} {
		n, err := f.ReadAt(buf, off)
		if err != nil && !errors.Is(err, io.EOF) {
			return [16]byte{}, err
		}
		_, _ = h.Write(buf[:n]) // hash.Hash.Write never returns an error
	}
	return sum(h), nil
}

func fullHash(p string, _ int64) ([16]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return [16]byte{}, err
	}
	defer f.Close()
	h := xxh3.New128()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return [16]byte{}, err
	}
	return sum(h), nil
}

// keeperOrder sorts a duplicate group so index 0 is the file to keep:
// non-copy-looking name, shallower path, older mtime, then lexical.
func keeperOrder(g []entry) {
	copyish := func(p string) bool {
		l := strings.ToLower(p)
		for _, s := range []string{"copy", "backup", ".bak", "/old/", "/tmp/", "(1)", "(2)", "~"} {
			if strings.Contains(l, s) {
				return true
			}
		}
		return false
	}
	sort.Slice(g, func(i, j int) bool {
		a, b := g[i], g[j]
		if ca, cb := copyish(a.Path), copyish(b.Path); ca != cb {
			return !ca
		}
		if da, db := strings.Count(a.Path, "/"), strings.Count(b.Path, "/"); da != db {
			return da < db
		}
		if a.Mtime != b.Mtime {
			return a.Mtime < b.Mtime
		}
		return a.Path < b.Path
	})
}

func gib(b int64) string { return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30)) }

func writeReports(es []entry, groups [][]entry, o opts) error {
	// catalog.jsonl: one record per file, same shape idea as the old share-catalog.jsonl
	cf, err := os.Create(filepath.Join(o.out, "catalog.jsonl"))
	if err != nil {
		return err
	}
	cw := bufio.NewWriterSize(cf, 1<<20)
	enc := json.NewEncoder(cw)
	for _, e := range es {
		if err := enc.Encode(map[string]any{
			"path": e.Path, "area": e.Area, "category": e.Cat,
			"mime": e.Mime, "size": e.Size, "mtime": e.Mtime, "atime": e.Atime, "stale": e.Stale,
		}); err != nil {
			return err
		}
	}
	if err := cw.Flush(); err != nil {
		return err
	}
	if err := cf.Close(); err != nil {
		return err
	}

	// dups.tsv
	for _, g := range groups {
		keeperOrder(g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i][0].Size*int64(len(groups[i])-1) > groups[j][0].Size*int64(len(groups[j])-1)
	})
	df, err := os.Create(filepath.Join(o.out, "dups.tsv"))
	if err != nil {
		return err
	}
	dw := bufio.NewWriter(df)
	fmt.Fprintln(dw, "group\taction\tsize\tmtime\tpath")
	var reclaim int64
	var dupFiles int
	for gi, g := range groups {
		for i, e := range g {
			act := "dup"
			if i == 0 {
				act = "keep"
			} else {
				reclaim += e.Size
				dupFiles++
			}
			fmt.Fprintf(dw, "%d\t%s\t%d\t%d\t%s\n", gi+1, act, e.Size, e.Mtime, strconv.Quote(e.Path))
		}
	}
	if err := dw.Flush(); err != nil {
		return err
	}
	if err := df.Close(); err != nil {
		return err
	}

	// stale.tsv, largest first
	var stale []entry
	var staleBytes int64
	for _, e := range es {
		if e.Stale != "" {
			stale = append(stale, e)
			staleBytes += e.Size
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].Size > stale[j].Size })
	sf, err := os.Create(filepath.Join(o.out, "stale.tsv"))
	if err != nil {
		return err
	}
	sw := bufio.NewWriter(sf)
	fmt.Fprintln(sw, "reason\tsize\tmtime\tatime\tpath")
	for _, e := range stale {
		fmt.Fprintf(sw, "%s\t%d\t%d\t%d\t%s\n", e.Stale, e.Size, e.Mtime, e.Atime, strconv.Quote(e.Path))
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	if err := sf.Close(); err != nil {
		return err
	}

	return writeSummary(es, o, len(groups), dupFiles, reclaim, len(stale), staleBytes)
}

type agg struct {
	files, bytes int64
	newest       int64
	cats         map[string]int64
}

func (a *agg) add(e entry) {
	if a.cats == nil {
		a.cats = map[string]int64{}
	}
	a.files++
	a.bytes += e.Size
	a.cats[e.Cat] += e.Size
	a.newest = max(a.newest, e.Mtime)
}

func (a *agg) top() string {
	best, bs := "", int64(-1)
	for c, s := range a.cats {
		if s > bs {
			best, bs = c, s
		}
	}
	return best
}

func writeSummary(es []entry, o opts, ng, nd int, reclaim int64, ns int, staleBytes int64) error {
	cats, areas, dirs, mimes := map[string]*agg{}, map[string]*agg{}, map[string]*agg{}, map[string]*agg{}
	get := func(m map[string]*agg, k string) *agg {
		if m[k] == nil {
			m[k] = &agg{}
		}
		return m[k]
	}
	var total int64
	for _, e := range es {
		total += e.Size
		get(cats, e.Cat).add(e)
		get(areas, e.Area).add(e)
		get(mimes, e.Mime).add(e)
		parts := strings.Split(strings.TrimPrefix(e.Path, o.root+"/"), "/")
		for d := 1; d < len(parts) && d <= o.depth; d++ {
			get(dirs, strings.Join(parts[:d], "/")).add(e)
		}
	}
	f, err := os.Create(filepath.Join(o.out, "report.md"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	fmt.Fprintf(w, "# %s report, %s\n\n- files: %d, bytes: %s\n- duplicate groups: %d, redundant files: %d, reclaimable: %s (min-size %s)\n- stale files (junk, or mtime older than %d d): %d, %s\n\n",
		o.root, time.Now().Format("2006-01-02"), len(es), gib(total), ng, nd, gib(reclaim),
		strconv.FormatInt(o.minSize>>20, 10)+" MiB", o.staleDays, ns, gib(staleBytes))
	table := func(title string, m map[string]*agg, n int) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]].bytes > m[keys[j]].bytes })
		fmt.Fprintf(w, "## %s\n\n| name | files | size | dominant category | newest mtime |\n|:---|---:|---:|:---|:---|\n", title)
		for i, k := range keys {
			if i == n {
				break
			}
			a := m[k]
			fmt.Fprintf(w, "| %s | %d | %s | %s | %s |\n", k, a.files, gib(a.bytes), a.top(),
				time.Unix(a.newest, 0).UTC().Format("2006-01-02"))
		}
		fmt.Fprintln(w)
	}
	cut := time.Now().AddDate(0, 0, -o.staleDays).Unix()
	cold := map[string]*agg{}
	for k, a := range dirs {
		if a.newest < cut {
			cold[k] = a
		}
	}
	table("By category", cats, 50)
	table("By MIME (content), 40 largest", mimes, 40)
	table("By top-level area", areas, 50)
	table(fmt.Sprintf("Directories (depth <= %d), 60 largest", o.depth), dirs, 60)
	table(fmt.Sprintf("Cold directories (nothing modified in %d d), 60 largest", o.staleDays), cold, 60)
	return nil
}

// ---- apply ----

type row struct {
	act, path string
	size, mt  int64
}

func runApply(args []string) int {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	plan := fs.String("plan", "", "dups.tsv from scan")
	mode := fs.String("mode", "quarantine", "quarantine: move dups aside; dedupe: share extents with the keeper (XFS reflink), paths stay")
	q := fs.String("quarantine", "", "quarantine dir on the SAME filesystem as the files (mode quarantine; manifest dir for dedupe)")
	limit := fs.Int("limit", 0, "stop after N files (canary run), 0 = all")
	yes := fs.Bool("yes", false, "perform the changes (default: dry run)")
	_ = fs.Parse(args) // flag.ExitOnError exits on a parse error
	if *plan == "" || *q == "" || (*mode != "quarantine" && *mode != "dedupe") {
		return fail(errors.New("-plan and -quarantine are required, -mode is quarantine|dedupe"))
	}
	f, err := os.Open(*plan)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	if err := os.MkdirAll(*q, 0o755); err != nil {
		return fail(err)
	}
	qst, _ := os.Stat(*q)
	man, err := os.OpenFile(filepath.Join(*q, "MANIFEST.tsv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fail(err)
	}
	defer man.Close()

	groups := map[string][]row{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	sc.Scan() // header
	for sc.Scan() {
		c := strings.Split(sc.Text(), "\t")
		if len(c) != 5 {
			continue
		}
		p, err := strconv.Unquote(c[4])
		if err != nil {
			continue
		}
		sz, _ := strconv.ParseInt(c[2], 10, 64)
		mt, _ := strconv.ParseInt(c[3], 10, 64)
		if _, ok := groups[c[0]]; !ok {
			order = append(order, c[0])
		}
		groups[c[0]] = append(groups[c[0]], row{c[1], p, sz, mt})
	}
	var moved, skipped int
	var bytes int64
	free := func() uint64 {
		var st syscall.Statfs_t
		_ = syscall.Statfs(*q, &st)
		return st.Bavail * uint64(st.Bsize)
	}
	freeBefore := free()
	for _, id := range order {
		var keeper *row
		for i, r := range groups[id] {
			if r.act == "keep" {
				keeper = &groups[id][i]
			}
		}
		if keeper == nil || !unchanged(*keeper) {
			skipped += len(groups[id])
			continue
		}
		for _, r := range groups[id] {
			if r.act != "dup" {
				continue
			}
			if *limit > 0 && moved >= *limit {
				break
			}
			if !unchanged(r) || !sameDev(r.path, qst) || !sameDev(keeper.path, qst) {
				skipped++
				continue
			}
			if *mode == "dedupe" {
				if !*yes {
					fmt.Printf("DRY dedupe %s <- %s\n", r.path, keeper.path)
				} else if err := dedupe(keeper.path, r.path, r.size); err != nil {
					fmt.Fprintf(os.Stderr, "warn: dedupe %s: %v\n", r.path, err)
					skipped++
					continue
				} else {
					fmt.Fprintf(man, "%s\tdedupe\t%s\t%s\n", id, strconv.Quote(r.path), strconv.Quote(keeper.path))
				}
				moved++
				bytes += r.size
				continue
			}
			dst := filepath.Join(*q, r.path)
			if !*yes {
				fmt.Printf("DRY %s -> %s\n", r.path, dst)
			} else {
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return fail(err)
				}
				if err := os.Rename(r.path, dst); err != nil {
					fmt.Fprintln(os.Stderr, "warn:", err)
					skipped++
					continue
				}
				fmt.Fprintf(man, "%s\t%s\t%s\n", id, strconv.Quote(r.path), strconv.Quote(dst))
			}
			moved++
			bytes += r.size
		}
	}
	fmt.Printf("changed=%d skipped=%d bytes=%s dry=%v free_before=%s free_after=%s\n",
		moved, skipped, gib(bytes), !*yes, gib(int64(freeBefore)), gib(int64(free())))
	return 0
}

// dedupe makes dup share its extents with keeper through FIDEDUPERANGE. The
// kernel compares the bytes itself and refuses (status DIFFERS) if they differ,
// so no content can be lost; mtime, owner, ACLs and the path of dup are kept.
func dedupe(keeper, dup string, size int64) error {
	src, err := os.Open(keeper)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dup, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer dst.Close()
	const chunk = 16 << 20
	for off := int64(0); off < size; {
		r := unix.FileDedupeRange{
			Src_offset: uint64(off), Src_length: uint64(min(chunk, size-off)),
			Info: []unix.FileDedupeRangeInfo{{Dest_fd: int64(dst.Fd()), Dest_offset: uint64(off)}},
		}
		if err := unix.IoctlFileDedupeRange(int(src.Fd()), &r); err != nil {
			return err
		}
		switch i := r.Info[0]; {
		case i.Status == unix.FILE_DEDUPE_RANGE_DIFFERS:
			return errors.New("content differs")
		case i.Status != unix.FILE_DEDUPE_RANGE_SAME:
			return syscall.Errno(-i.Status)
		case i.Bytes_deduped == 0:
			return errors.New("no progress")
		default:
			off += int64(i.Bytes_deduped)
		}
	}
	return nil
}

// unchanged guards against the file having been modified since the scan.
func unchanged(r row) bool {
	st, err := os.Lstat(r.path)
	return err == nil && st.Mode().IsRegular() && st.Size() == r.size && st.ModTime().Unix() == r.mt
}

func sameDev(p string, q os.FileInfo) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Sys().(*syscall.Stat_t).Dev == q.Sys().(*syscall.Stat_t).Dev
}
