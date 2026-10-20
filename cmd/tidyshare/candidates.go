package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Deletion candidates in three tiers. Nothing here deletes anything.
//
//	1  safe:    duplicate copies (a kept copy exists), junk, wholly redundant dirs
//	2  review:  image archives already in container storage, regenerable caches
//	3  decide:  trees untouched for -stale-days, regenerable build output
var regen = map[string]int{
	"node_modules": 2, "__pycache__": 2, ".venv": 2, "venv": 2, ".tox": 2, ".cache": 2, ".gradle": 2,
	"target": 3, "build": 3, "dist": 3, "extracted": 3, "extracts": 3, "extract": 3,
}

type dstat struct{ files, bytes, dupFiles, dupBytes, newest int64 }

func parent(p, root string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= len(root) {
		return ""
	}
	return p[:i]
}

func ancestors(p, root string, f func(d string)) {
	for d := parent(p, root); d != ""; d = parent(d, root) {
		f(d)
	}
}

func writeCandidates(es []entry, groups [][]entry, ir imageReport, o opts) error {
	cut := time.Now().AddDate(0, 0, -o.staleDays).Unix()
	dup := map[string]bool{}
	for _, g := range groups {
		for _, e := range g[1:] {
			dup[e.Path] = true
		}
	}
	archCovered := map[string]bool{}
	for _, a := range ir.Archives {
		if a.Status == "in-store" {
			archCovered[a.Path] = true
		}
	}

	dirs := map[string]*dstat{}
	for _, e := range es {
		d := dup[e.Path]
		ancestors(e.Path, o.root, func(dir string) {
			s := dirs[dir]
			if s == nil {
				s = &dstat{}
				dirs[dir] = s
			}
			s.files++
			s.bytes += e.Size
			s.newest = max(s.newest, e.Mtime)
			if d {
				s.dupFiles++
				s.dupBytes += e.Size
			}
		})
	}
	// top-most directories only: skip a dir when an ancestor already qualifies
	topmost := func(ok func(d string, s *dstat) bool) []string {
		var sel []string
		for d, s := range dirs {
			if ok(d, s) {
				sel = append(sel, d)
			}
		}
		set := map[string]bool{}
		for _, d := range sel {
			set[d] = true
		}
		var out []string
		for _, d := range sel {
			nested := false
			ancestors(d, o.root, func(a string) { nested = nested || set[a] })
			if !nested {
				out = append(out, d)
			}
		}
		sort.Slice(out, func(i, j int) bool { return dirs[out[i]].bytes > dirs[out[j]].bytes })
		return out
	}
	redundant := topmost(func(_ string, s *dstat) bool { return s.files > 0 && s.dupFiles == s.files })
	regenDirs := topmost(func(d string, _ *dstat) bool { return regen[filepath.Base(d)] > 0 })
	coldDirs := topmost(func(_ string, s *dstat) bool { return s.newest < cut && s.bytes >= 100<<20 })

	// per-file tier (lowest wins) so the summary does not double count
	regenSet := map[string]bool{}
	for _, d := range regenDirs {
		regenSet[d] = true
	}
	var tierBytes, tierFiles [4]int64
	for _, e := range es {
		t := 0
		if dup[e.Path] || e.Stale == "junk" {
			t = 1
		} else {
			inRegen := 0
			ancestors(e.Path, o.root, func(a string) {
				if regenSet[a] {
					inRegen = regen[filepath.Base(a)]
				}
			})
			switch {
			case archCovered[e.Path] || inRegen == 2:
				t = 2
			case inRegen == 3 || e.Stale == "cold":
				t = 3
			}
		}
		if t > 0 {
			tierBytes[t] += e.Size
			tierFiles[t]++
		}
	}

	// dup bytes per directory at depth <= 4 (what to look at first)
	byDir := map[string]*dstat{}
	for _, g := range groups {
		for _, e := range g[1:] {
			rel := strings.TrimPrefix(e.Path, o.root+"/")
			parts := strings.Split(rel, "/")
			n := min(len(parts)-1, 4)
			k := strings.Join(parts[:n], "/")
			if byDir[k] == nil {
				byDir[k] = &dstat{}
			}
			byDir[k].dupFiles++
			byDir[k].dupBytes += e.Size
		}
	}
	dkeys := make([]string, 0, len(byDir))
	for k := range byDir {
		dkeys = append(dkeys, k)
	}
	sort.Slice(dkeys, func(i, j int) bool { return byDir[dkeys[i]].dupBytes > byDir[dkeys[j]].dupBytes })

	tsv, err := os.Create(filepath.Join(o.out, "candidates.tsv"))
	if err != nil {
		return err
	}
	tw := bufio.NewWriter(tsv)
	fmt.Fprintln(tw, "tier\tkind\tbytes\tfiles\tpath\tnote")
	row := func(t int, kind string, b, n int64, p, note string) {
		fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%s\t%s\n", t, kind, b, n, strconv.Quote(p), note)
	}
	for _, g := range groups {
		for _, e := range g[1:] {
			row(1, "dup-file", e.Size, 1, e.Path, "keep: "+g[0].Path)
		}
	}
	for _, e := range es {
		if e.Stale == "junk" {
			row(1, "junk-file", e.Size, 1, e.Path, "")
		}
	}
	for _, d := range redundant {
		row(1, "redundant-dir", dirs[d].bytes, dirs[d].files, d, "every file is a duplicate of a kept copy")
	}
	for _, a := range ir.Archives {
		if a.Status == "in-store" {
			row(2, "image-archive", a.Size, 1, a.Path, "images present in "+strings.Join(a.Stores, ","))
		}
	}
	for _, d := range regenDirs {
		row(regen[filepath.Base(d)], "regen-dir", dirs[d].bytes, dirs[d].files, d, filepath.Base(d))
	}
	for _, d := range coldDirs {
		row(3, "cold-dir", dirs[d].bytes, dirs[d].files, d, "newest mtime "+time.Unix(dirs[d].newest, 0).UTC().Format("2006-01-02"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if err := tsv.Close(); err != nil {
		return err
	}

	md, err := os.Create(filepath.Join(o.out, "candidates.md"))
	if err != nil {
		return err
	}
	defer md.Close()
	w := bufio.NewWriter(md)
	defer w.Flush()
	fmt.Fprintf(w, "# Deletion candidates for %s\n\nNothing is deleted by the tool. Each file is counted once, in its lowest tier.\n\n| tier | meaning | files | size |\n|:---|:---|---:|---:|\n", o.root)
	for t := 1; t <= 3; t++ {
		fmt.Fprintf(w, "| %d | %s | %d | %s |\n", t, []string{"", "safe: duplicate copy, junk", "review: image archive in storage, cache", fmt.Sprintf("decide: untouched %d d, regenerable output", o.staleDays)}[t], tierFiles[t], gib(tierBytes[t]))
	}
	list := func(title string, paths []string, n int, note func(string) string) {
		fmt.Fprintf(w, "\n## %s\n\n| size | files | path | note |\n|---:|---:|:---|:---|\n", title)
		for i, d := range paths {
			if i == n {
				break
			}
			fmt.Fprintf(w, "| %s | %d | %s | %s |\n", gib(dirs[d].bytes), dirs[d].files, d, note(d))
		}
	}
	list("Tier 1: directories whose every file is a duplicate", redundant, 50, func(string) string { return "" })
	fmt.Fprint(w, "\n## Tier 1: duplicate bytes per directory (depth <= 4)\n\n| reclaimable | dup files | directory |\n|---:|---:|:---|\n")
	for i, k := range dkeys {
		if i == 50 {
			break
		}
		fmt.Fprintf(w, "| %s | %d | %s |\n", gib(byDir[k].dupBytes), byDir[k].dupFiles, k)
	}
	fmt.Fprint(w, "\n## Tier 1: largest duplicate files (keeper in the note)\n\n| size | path | keep |\n|---:|:---|:---|\n")
	n := 0
	for _, g := range groups { // groups are sorted by reclaimable bytes
		for _, e := range g[1:] {
			if n++; n > 40 {
				break
			}
			fmt.Fprintf(w, "| %s | %s | %s |\n", gib(e.Size), e.Path, g[0].Path)
		}
	}
	fmt.Fprint(w, "\n## Tier 2: image archives already in container storage\n\n| size | path | stores | tags |\n|---:|:---|:---|:---|\n")
	for _, a := range ir.Archives {
		if a.Status == "in-store" {
			fmt.Fprintf(w, "| %s | %s | %s | %s |\n", gib(a.Size), a.Path, strings.Join(a.Stores, ","), strings.Join(a.Tags, " "))
		}
	}
	var r2, r3 []string
	for _, d := range regenDirs {
		if regen[filepath.Base(d)] == 2 {
			r2 = append(r2, d)
		} else {
			r3 = append(r3, d)
		}
	}
	list("Tier 2: regenerable caches (node_modules, __pycache__, .venv, ...)", r2, 30, func(d string) string { return filepath.Base(d) })
	list("Tier 3: regenerable build output (target, build, dist, extracted, ...)", r3, 40, func(d string) string { return filepath.Base(d) })
	list(fmt.Sprintf("Tier 3: top-most trees with nothing modified in %d d (>= 100 MiB)", o.staleDays), coldDirs, 50,
		func(d string) string { return "newest " + time.Unix(dirs[d].newest, 0).UTC().Format("2006-01-02") })
	return nil
}
