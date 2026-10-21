package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type cleanupResult struct {
	Target string `json:"target"`
	Keeper string `json:"keeper,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

type cleanupReport struct {
	SchemaVersion int             `json:"schema_version"`
	Operation     string          `json:"operation"`
	DryRun        bool            `json:"dry_run"`
	Results       []cleanupResult `json:"results"`
	Changed       int             `json:"changed"`
	Skipped       int             `json:"skipped"`
	LogicalBytes  int64           `json:"logical_bytes"`
}

func parseReportFormats(value string) ([]string, error) {
	var formats []string
	seen := map[string]bool{}
	for part := range strings.SplitSeq(value, ",") {
		format := strings.TrimSpace(part)
		if format != "json" && format != "jsonl" && format != "md" {
			return nil, fmt.Errorf("unsupported report format %q: use json,jsonl,md", format)
		}
		if !seen[format] {
			formats = append(formats, format)
			seen[format] = true
		}
	}
	return formats, nil
}

func openReportDirectory(out string) (*os.File, error) {
	return openPlainDirectory(out, true)
}

// openPlainDirectory pins every directory component without following symlinks.
func openPlainDirectory(path string, create bool) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for component := range strings.SplitSeq(strings.TrimPrefix(abs, "/"), "/") {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if create && errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(fd)
				return nil, fmt.Errorf("create plain directory: %w", mkdirErr)
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("open directory without symlinks: %w", openErr)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), abs), nil
}

// reserveReports opens all destinations before cleanup can change user data.
func reserveReports(out string, names []string) (map[string]*os.File, func(), error) {
	dir, err := openReportDirectory(out)
	if err != nil {
		return nil, nil, err
	}
	defer dir.Close()
	for _, name := range names {
		if filepath.Base(name) != name || name == "." || name == ".." {
			return nil, nil, errors.New("invalid report filename")
		}
		var st unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
			if err == nil {
				return nil, nil, fmt.Errorf("report destination already exists: %s", name)
			}
			return nil, nil, fmt.Errorf("check report destination: %w", err)
		}
	}
	files := make(map[string]*os.File, len(names))
	closeFiles := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for _, name := range names {
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			closeFiles()
			return nil, nil, fmt.Errorf("reserve report destination: %w", err)
		}
		files[name] = os.NewFile(uintptr(fd), filepath.Join(out, name))
	}
	return files, closeFiles, nil
}

func prepareOperationReports(out string, formats []string) (finish func(cleanupReport) error, closeReports func(), err error) {
	formats, err = parseReportFormats(strings.Join(formats, ","))
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(formats))
	for _, format := range formats {
		names = append(names, "cleanup."+format)
	}
	files, closeFiles, err := reserveReports(out, names)
	if err != nil {
		return nil, nil, err
	}
	finished := false
	finish = func(report cleanupReport) error {
		if finished {
			return errors.New("operation reports already finished")
		}
		finished = true
		defer closeFiles()
		if report.SchemaVersion == 0 {
			report.SchemaVersion = 1
		}
		if report.Results == nil {
			report.Results = []cleanupResult{}
		}
		for _, format := range formats {
			f := files["cleanup."+format]
			if err := encodeOperationReport(f, format, report); err != nil {
				return fmt.Errorf("write cleanup report: %w", err)
			}
			if err := f.Sync(); err != nil {
				return fmt.Errorf("sync cleanup report: %w", err)
			}
		}
		return nil
	}
	return finish, closeFiles, nil
}

func writeOperationReports(out string, formats []string, report cleanupReport) error {
	finish, closeReports, err := prepareOperationReports(out, formats)
	if err != nil {
		return err
	}
	defer closeReports()
	return finish(report)
}

func encodeOperationReport(w io.Writer, format string, report cleanupReport) error {
	enc := json.NewEncoder(w)
	switch format {
	case "json":
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "jsonl":
		summary := report
		summary.Results = nil
		if err := enc.Encode(struct {
			Type string `json:"type"`
			cleanupReport
		}{"summary", summary}); err != nil {
			return err
		}
		for _, result := range report.Results {
			if err := enc.Encode(struct {
				Type          string `json:"type"`
				SchemaVersion int    `json:"schema_version"`
				cleanupResult
			}{"result", report.SchemaVersion, result}); err != nil {
				return err
			}
		}
		return nil
	case "md":
		b := bufio.NewWriter(w)
		fmt.Fprintf(b, "# Cleanup report\n\nSchema version: %d\n\nOperation: %s\n\nDry run: %t\n\nChanged: %d; skipped: %d; logical bytes: %d\n\n", report.SchemaVersion, markdownCell(report.Operation), report.DryRun, report.Changed, report.Skipped, report.LogicalBytes)
		fmt.Fprintln(b, "| target | keeper | owner | status | reason | bytes |\n| --- | --- | --- | --- | --- | ---: |")
		for _, r := range report.Results {
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %d |\n", markdownCell(r.Target), markdownCell(r.Keeper), markdownCell(r.Owner), markdownCell(r.Status), markdownCell(r.Reason), r.Bytes)
		}
		return b.Flush()
	default:
		return errors.New("unsupported report format")
	}
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", "\\r")
	value = strings.ReplaceAll(value, "\t", "\\t")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return strings.ReplaceAll(value, ">", "&gt;")
}

type scanRecord struct {
	SchemaVersion int            `json:"schema_version"`
	Type          string         `json:"type"`
	Data          map[string]any `json:"data"`
}

// publishScanReports keeps legacy renderers isolated from user-selected paths.
func publishScanReports(stage, out string, formats []string, root string) error {
	selected := map[string]bool{}
	for _, format := range formats {
		selected[format] = true
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	var names []string
	var sources []string
	recordCount := 0
	for _, file := range entries {
		name := file.Name()
		ext := strings.TrimPrefix(filepath.Ext(name), ".")
		if selected[ext] || (ext != "md" && ext != "json" && ext != "jsonl") {
			names = append(names, name)
		}
		if (selected["json"] || selected["jsonl"]) && (ext == "tsv" || name == "catalog.jsonl") {
			count, err := countScanRecords(filepath.Join(stage, name))
			if err != nil {
				return err
			}
			recordCount += count
			sources = append(sources, name)
		}
	}
	for _, format := range []string{"json", "jsonl"} {
		if selected[format] {
			names = append(names, "report."+format)
		}
	}
	files, closeFiles, err := reserveReports(out, names)
	if err != nil {
		return err
	}
	defer closeFiles()
	for _, name := range names {
		f := files[name]
		if name == "report.json" || name == "report.jsonl" {
			continue
		}
		src, err := os.Open(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		_, err = io.Copy(f, src)
		err = errors.Join(err, src.Close())
		if err != nil {
			return fmt.Errorf("publish scan report: %w", err)
		}
	}
	if err := writeScanMachineReports(stage, root, sources, recordCount, files); err != nil {
		return fmt.Errorf("publish scan report: %w", err)
	}
	for _, f := range files {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func countScanRecords(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 1<<20)
	catalog := filepath.Ext(path) == ".jsonl"
	columns := 0
	if !catalog && sc.Scan() {
		columns = strings.Count(sc.Text(), "\t")
	}
	count := 0
	for sc.Scan() {
		if (catalog && !json.Valid(sc.Bytes())) || (!catalog && strings.Count(sc.Text(), "\t") != columns) {
			return 0, fmt.Errorf("invalid staged report row in %s", filepath.Base(path))
		}
		count++
	}
	return count, sc.Err()
}

func writeScanMachineReports(stage, root string, sources []string, count int, files map[string]*os.File) error {
	var jsonWriter, jsonlWriter *bufio.Writer
	var jsonEncoder, jsonlEncoder *json.Encoder
	if file := files["report.json"]; file != nil {
		jsonWriter = bufio.NewWriter(file)
		jsonEncoder = json.NewEncoder(jsonWriter)
		rootJSON, err := json.Marshal(root)
		if err != nil {
			return err
		}
		fmt.Fprintf(jsonWriter, "{\"schema_version\":1,\"operation\":\"scan\",\"root\":%s,\"records\":[", rootJSON)
	}
	if file := files["report.jsonl"]; file != nil {
		jsonlWriter = bufio.NewWriter(file)
		jsonlEncoder = json.NewEncoder(jsonlWriter)
		if err := jsonlEncoder.Encode(scanRecord{1, "summary", map[string]any{"operation": "scan", "root": root, "records": count}}); err != nil {
			return err
		}
	}
	first := true
	for _, source := range sources {
		if err := forEachScanRecord(filepath.Join(stage, source), strings.TrimSuffix(source, filepath.Ext(source)), func(record scanRecord) error {
			if jsonEncoder != nil {
				if !first {
					if err := jsonWriter.WriteByte(','); err != nil {
						return err
					}
				}
				if err := jsonEncoder.Encode(record); err != nil {
					return err
				}
			}
			first = false
			if jsonlEncoder != nil {
				return jsonlEncoder.Encode(record)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if jsonWriter != nil {
		if _, err := jsonWriter.WriteString("]}\n"); err != nil {
			return err
		}
		if err := jsonWriter.Flush(); err != nil {
			return err
		}
	}
	if jsonlWriter != nil {
		return jsonlWriter.Flush()
	}
	return nil
}

func forEachScanRecord(path, kind string, consume func(scanRecord) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if kind == "catalog" {
		dec := json.NewDecoder(f)
		dec.UseNumber()
		for {
			var data map[string]any
			if err := dec.Decode(&data); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			if err := consume(scanRecord{1, kind, data}); err != nil {
				return err
			}
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 1<<20)
	if !sc.Scan() {
		return sc.Err()
	}
	columns := strings.Split(sc.Text(), "\t")
	for sc.Scan() {
		values := strings.Split(sc.Text(), "\t")
		if len(values) != len(columns) {
			return fmt.Errorf("invalid staged report row in %s", filepath.Base(path))
		}
		data := make(map[string]any, len(columns))
		for i, column := range columns {
			value := values[i]
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
			data[column] = value
		}
		if err := consume(scanRecord{1, kind, data}); err != nil {
			return err
		}
	}
	return sc.Err()
}
