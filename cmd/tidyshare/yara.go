package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

type yaraDiagnosticWriter struct {
	io.Writer
	cancel context.CancelFunc
}

func (w *yaraDiagnosticWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if n < len(data) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.cancel()
	}
	return n, err
}

// runYara scans selected categories with the configured classic yara CLI.
func runYara(ctx context.Context, es []entry, o opts) (hits []yaraHit, resultErr error) {
	if o.yaraRules == "" {
		return nil, ctx.Err()
	}
	defer func() {
		if managedUncertain(resultErr) {
			resultErr = retainManagedRoot(resultErr, o.out)
		}
	}()
	rules, err := rulesFile(ctx, o)
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
	if err := finishReport(w, lf); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "yara: scanning %d files\n", len(sizes))

	errf, err := os.Create(filepath.Join(o.out, "yara.err"))
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, errf.Close()) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := exec.Command(o.yaraBin, "-w", "-f", "-p", strconv.Itoa(o.workers), "-a", "120",
		"--scan-list", rules, lf.Name())
	c.Stderr = &yaraDiagnosticWriter{errf, cancel}
	reader, writer := io.Pipe()
	c.Stdout = writer
	scanned := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(reader)
		sc.Buffer(make([]byte, 4096), 1<<20)
		for sc.Scan() {
			rule, path, ok := strings.Cut(sc.Text(), " ")
			if ok {
				hits = append(hits, yaraHit{rule, path, sizes[path]})
			}
		}
		err := sc.Err()
		_ = reader.CloseWithError(err)
		if err != nil {
			cancel()
		}
		scanned <- err
	}()
	_, runErr := runOwnedCommand(ctx, c, managedProcessOps{})
	_ = writer.Close()
	scanErr := <-scanned
	writeErr := writeYara(hits, o)
	if err := errors.Join(runErr, scanErr, writeErr); err != nil {
		return hits, fmt.Errorf("YARA stage failed (see yara.err): %w", err)
	}
	return hits, nil
}

// rulesFile returns the rules path; a directory becomes a generated index of
// include lines (classic yara takes one rules file).
func rulesFile(ctx context.Context, o opts) (path string, resultErr error) {
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
	defer func() { resultErr = errors.Join(resultErr, f.Close()) }()
	// accept a file only if it compiles together with the ones accepted so far
	// (the rule sets overlap: duplicated identifiers, dialect-specific syntax)
	var accepted []string
	err = filepath.WalkDir(o.yaraRules, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || (!strings.HasSuffix(p, ".yar") && !strings.HasSuffix(p, ".yara")) {
			return err
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		try := filepath.Join(o.out, "yara-try.yar")
		if werr := os.WriteFile(try, []byte(includes(append(accepted, abs))), 0o600); werr != nil {
			return werr
		}
		var output bytes.Buffer
		command := exec.Command(o.yaraBin, "-w", try, os.DevNull)
		command.Stdout = &output
		command.Stderr = &output
		if _, e := runOwnedCommand(ctx, command, managedProcessOps{}); e != nil {
			if managedUncertain(e) || ctx.Err() != nil {
				return e
			}
			fmt.Fprintf(os.Stderr, "warn: yara rules skipped: %s: %s\n", abs, firstLine(output.Bytes()))
			return nil
		}
		accepted = append(accepted, abs)
		return nil
	})
	if !managedUncertain(err) {
		_ = os.Remove(filepath.Join(o.out, "yara-try.yar"))
	}
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(includes(accepted))
	return idx, err
}

func writeYara(hits []yaraHit, o opts) (resultErr error) {
	f, err := os.Create(filepath.Join(o.out, "yara.tsv"))
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	defer func() { resultErr = errors.Join(resultErr, finishReport(w, f)) }()
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
