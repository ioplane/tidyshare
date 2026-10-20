package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type yaraHit struct {
	Rule, Path string
	Size       int64
}

// runYara scans only the selected categories with the classic `yara` CLI
// (--scan-list). ponytail: YARA-X's `yr scan` documents no scan-list option,
// so yr is not driven here; add a per-directory mode if it is needed.
func runYara(es []entry, o opts) ([]yaraHit, error) {
	if o.yaraRules == "" {
		return nil, nil
	}
	rules, err := rulesFile(o)
	if err != nil {
		return nil, err
	}
	cats := map[string]bool{}
	for _, c := range splitList(o.yaraCats) {
		cats[c] = true
	}
	sizes := map[string]int64{}
	lf, err := os.Create(filepath.Join(o.out, "yara.list"))
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriter(lf)
	for _, e := range es {
		if cats[e.Cat] && e.Size <= o.yaraMax && !strings.ContainsRune(e.Path, '\n') {
			fmt.Fprintln(w, e.Path)
			sizes[e.Path] = e.Size
		}
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}
	if err := lf.Close(); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "yara: scanning %d files\n", len(sizes))

	errf, _ := os.Create(filepath.Join(o.out, "yara.err"))
	defer errf.Close()
	c := exec.Command(o.yaraBin, "-w", "-f", "-p", strconv.Itoa(o.workers), "-a", "120",
		"--scan-list", rules, lf.Name())
	c.Stderr = errf
	out, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	var hits []yaraHit
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		rule, path, ok := strings.Cut(sc.Text(), " ") // rule names contain no spaces
		if ok {
			hits = append(hits, yaraHit{rule, path, sizes[path]})
		}
	}
	// yara exits non-zero on unreadable files; hits already collected stay valid
	if err := c.Wait(); err != nil && len(hits) == 0 {
		return nil, fmt.Errorf("%s: %w (see yara.err)", o.yaraBin, err)
	}
	return hits, writeYara(hits, o)
}

// rulesFile returns the rules path; a directory becomes a generated index of
// include lines (classic yara takes one rules file).
func rulesFile(o opts) (string, error) {
	st, err := os.Stat(o.yaraRules)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return o.yaraRules, nil
	}
	idx := filepath.Join(o.out, "yara-index.yar")
	f, err := os.Create(idx)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// accept a file only if it compiles together with the ones accepted so far
	// (the rule sets overlap: duplicated identifiers, dialect-specific syntax)
	var accepted []string
	err = filepath.WalkDir(o.yaraRules, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || (!strings.HasSuffix(p, ".yar") && !strings.HasSuffix(p, ".yara")) {
			return err
		}
		abs, _ := filepath.Abs(p)
		try := filepath.Join(o.out, "yara-try.yar")
		if werr := os.WriteFile(try, []byte(includes(append(accepted, abs))), 0o644); werr != nil {
			return werr
		}
		if out, e := exec.Command(o.yaraBin, "-w", try, os.DevNull).CombinedOutput(); e != nil {
			fmt.Fprintf(os.Stderr, "warn: yara rules skipped: %s: %s\n", abs, firstLine(out))
			return nil
		}
		accepted = append(accepted, abs)
		return nil
	})
	_ = os.Remove(filepath.Join(o.out, "yara-try.yar"))
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(includes(accepted))
	return idx, err
}

func writeYara(hits []yaraHit, o opts) error {
	f, err := os.Create(filepath.Join(o.out, "yara.tsv"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	fmt.Fprintln(w, "rule\tsize\tpath")
	count := map[string]int{}
	for _, h := range hits {
		fmt.Fprintf(w, "%s\t%d\t%s\n", h.Rule, h.Size, strconv.Quote(h.Path))
		count[h.Rule]++
	}
	// per-rule counts to stderr for a quick look
	rules := make([]string, 0, len(count))
	for r := range count {
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return count[rules[i]] > count[rules[j]] })
	for i, r := range rules {
		if i == 15 {
			break
		}
		fmt.Fprintf(os.Stderr, "yara: %6d %s\n", count[r], r)
	}
	return nil
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

func includes(files []string) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "include %q\n", f)
	}
	return b.String()
}
