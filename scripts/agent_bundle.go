//go:build ignore

// Build or check the version-matched agent bundle. This tool does not install it.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	bundleRoot       = "tidyshare-agent-bundle"
	bundleFileLimit  = 8 << 20
	bundleTotalLimit = 64 << 20
)

var payloadFiles = []string{
	"SKILL.md", "README.md", "LICENSE", "NOTICE",
	"workflows/inventory-review.md", "workflows/managed-cleanup.md", "workflows/recovery-diagnostics.md",
	"workflows/adapter-development.md", "workflows/testing.md", "workflows/release.md",
	"examples/cli.md", "examples/codex-mcp.json", "examples/claude-code-mcp.json",
}

var (
	rawVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	numericIdentifier = regexp.MustCompile(`^[0-9]+$`)
)

type bundleManifest struct {
	Version string            `json:"version"`
	Commit  string            `json:"commit"`
	Files   map[string]string `json:"files"`
}

func validateBundleIdentity(version, commit string) error {
	if !rawVersionPattern.MatchString(version) {
		return errors.New("version must be a valid version without a leading v")
	}
	// Snapshot identity must preserve every full source SHA, including numeric SHA values.
	if _, prerelease, ok := strings.Cut(version, "-"); ok && version != "0.0.0-dev."+commit {
		for _, identifier := range strings.Split(prerelease, ".") {
			if len(identifier) > 1 && identifier[0] == '0' && numericIdentifier.MatchString(identifier) {
				return errors.New("numeric prerelease identifiers must not have leading zeroes")
			}
		}
	}
	if !commitPattern.MatchString(commit) {
		return errors.New("commit must be the full lowercase source SHA")
	}
	return nil
}

func releaseVersion(kind, ref, commit string) (string, error) {
	version := "0.0.0-dev." + commit
	switch kind {
	case "tag":
		if !strings.HasPrefix(ref, "v") {
			return "", errors.New("release tag must start with v")
		}
		version = strings.TrimPrefix(ref, "v")
	case "branch":
	default:
		return "", errors.New("ref type must be tag or branch")
	}
	return version, validateBundleIdentity(version, commit)
}

func manifestBytes(manifest bundleManifest) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	return append(data, '\n'), err
}

func buildBundle(source, out, version, commit string) (string, error) {
	if err := validateBundleIdentity(version, commit); err != nil {
		return "", err
	}
	root, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("bundle source must be a plain directory")
	}
	allowed := map[string]bool{}
	for _, name := range payloadFiles {
		allowed[name] = true
	}
	payload := map[string][]byte{}
	total := 0
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if entry.IsDir() {
			if name != "." && name != "workflows" && name != "examples" {
				return fmt.Errorf("unexpected bundle directory %q", name)
			}
			return nil
		}
		if !allowed[name] {
			return fmt.Errorf("unexpected bundle source file %q", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 || info.Size() <= 0 || info.Size() > bundleFileLimit {
			return fmt.Errorf("bundle source must be a bounded regular non-executable file: %s", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += len(data)
		if total > bundleTotalLimit {
			return errors.New("bundle payload exceeds size limit")
		}
		payload[name] = data
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, name := range payloadFiles {
		if payload[name] == nil {
			return "", fmt.Errorf("missing bundle file %s", name)
		}
	}
	payload["VERSION"] = []byte("v" + version + "\n")
	manifest := bundleManifest{Version: "v" + version, Commit: commit, Files: map[string]string{}}
	names := make([]string, 0, len(payload)+1)
	for name, data := range payload {
		sum := sha256.Sum256(data)
		manifest.Files[name] = hex.EncodeToString(sum[:])
		names = append(names, name)
	}
	payload["manifest.json"], err = manifestBytes(manifest)
	if err != nil {
		return "", err
	}
	names = append(names, "manifest.json")
	slices.Sort(names)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(out, ".agent-bundle-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	writeErr := tw.WriteHeader(&tar.Header{Name: bundleRoot + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR})
	for _, name := range names {
		if writeErr != nil {
			break
		}
		data := payload[name]
		writeErr = tw.WriteHeader(&tar.Header{Name: bundleRoot + "/" + name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR})
		if writeErr == nil {
			_, writeErr = tw.Write(data)
		}
	}
	writeErr = errors.Join(writeErr, tw.Close(), gz.Close(), file.Sync(), file.Close())
	if writeErr != nil {
		return "", writeErr
	}
	if err := os.Chmod(file.Name(), 0o644); err != nil {
		return "", err
	}
	destination := filepath.Join(out, "tidyshare_v"+version+"_agent-bundle.tar.gz")
	if err := os.Rename(file.Name(), destination); err != nil {
		return "", err
	}
	return destination, nil
}

func verifyBundle(archive, version, commit string) error {
	if err := validateBundleIdentity(version, commit); err != nil {
		return err
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	input := bufio.NewReader(file)
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gz.Close()
	gz.Multistream(false)
	if !gz.ModTime.IsZero() || gz.Name != "" || gz.Comment != "" || len(gz.Extra) != 0 || gz.OS != 255 {
		return errors.New("unexpected gzip header")
	}
	allowed := map[string]bool{"VERSION": true, "manifest.json": true}
	for _, name := range payloadFiles {
		allowed[name] = true
	}
	tr := tar.NewReader(gz)
	payload := map[string][]byte{}
	rootSeen := false
	previous := ""
	total := int64(0)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Name <= previous {
			return errors.New("duplicate or unsorted archive member")
		}
		previous = header.Name
		if header.Format != tar.FormatUSTAR || len(header.PAXRecords) != 0 || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || header.ModTime.Unix() != 0 {
			return errors.New("unexpected tar header")
		}
		if header.Name == bundleRoot+"/" {
			if rootSeen || header.Typeflag != tar.TypeDir || header.Mode != 0o755 || header.Size != 0 {
				return errors.New("invalid archive root")
			}
			rootSeen = true
			continue
		}
		name, ok := strings.CutPrefix(header.Name, bundleRoot+"/")
		if !ok || !allowed[name] || header.Typeflag != tar.TypeReg || header.Mode != 0o644 || header.Size <= 0 || header.Size > bundleFileLimit {
			return fmt.Errorf("unsafe or unexpected archive member %q", header.Name)
		}
		total += header.Size
		if total > bundleTotalLimit {
			return errors.New("bundle payload exceeds size limit")
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		payload[name] = data
	}
	// Consume the gzip checksum. Reject additional gzip members or hidden tar data.
	remainder, err := io.ReadAll(io.LimitReader(gz, 1025))
	if err != nil {
		return err
	}
	if len(remainder) > 1024 || len(bytes.Trim(remainder, "\x00")) != 0 {
		return errors.New("unexpected data after tar members")
	}
	if _, err := input.Peek(1); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after gzip member")
	}
	if !rootSeen || len(payload) != len(allowed) {
		return errors.New("bundle is incomplete")
	}
	for name := range allowed {
		if payload[name] == nil {
			return fmt.Errorf("missing archive member %s", name)
		}
	}
	var manifest bundleManifest
	decoder := json.NewDecoder(bytes.NewReader(payload["manifest.json"]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return err
	}
	canonical, err := manifestBytes(manifest)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, payload["manifest.json"]) {
		return errors.New("manifest is not canonical")
	}
	if manifest.Version != "v"+version || manifest.Commit != commit || string(payload["VERSION"]) != "v"+version+"\n" {
		return errors.New("bundle version or source commit mismatch")
	}
	if len(manifest.Files) != len(allowed)-1 {
		return errors.New("manifest file list is incomplete")
	}
	for name, data := range payload {
		if name == "manifest.json" {
			continue
		}
		sum := sha256.Sum256(data)
		if manifest.Files[name] != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("bundle checksum mismatch: %s", name)
		}
	}
	return nil
}

type binaryReceipt struct {
	Name          string `json:"name"`
	SHA256        string `json:"sha256"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	GoVersion     string `json:"go_version"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	CGOEnabled    string `json:"cgo_enabled"`
	Compiler      string `json:"compiler"`
	LDFlags       string `json:"ldflags"`
	VCSRevision   string `json:"vcs_revision,omitempty"`
	VCSModified   string `json:"vcs_modified,omitempty"`
	NativeVersion string `json:"native_version,omitempty"`
}

func releaseLDFlags(version, commit string) string {
	return "-s -w -X main.version=v" + version + " -X main.commit=" + commit
}

func releaseVersionOutput(version, commit string) string {
	return "tidyshare v" + version + " (" + commit + ")"
}

func readBinarySettings(path string) (*buildinfo.BuildInfo, map[string]string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		if _, exists := settings[setting.Key]; exists {
			return nil, nil, errors.New("duplicate build setting")
		}
		settings[setting.Key] = setting.Value
	}
	return info, settings, nil
}

type nativeVersionBuffer struct {
	data     []byte
	cancel   context.CancelFunc
	exceeded bool
}

func (b *nativeVersionBuffer) Write(data []byte) (int, error) {
	n := len(data)
	available := 4096 - len(b.data)
	if len(data) > available {
		data = data[:available]
		b.exceeded = true
		b.cancel()
	}
	b.data = append(b.data, data...)
	return n, nil
}

// Receipts bind declared build inputs to bytes. Native output is separate evidence.
func recordBinaryReceipt(binary, destination, version, commit, ldflags string, native bool) error {
	if err := validateBundleIdentity(version, commit); err != nil {
		return err
	}
	if ldflags != releaseLDFlags(version, commit) {
		return errors.New("build linker flags do not match prepared version and commit")
	}
	stat, err := os.Lstat(binary)
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return errors.New("binary must be a plain file")
	}
	info, settings, err := readBinarySettings(binary)
	if err != nil {
		return err
	}
	name := "tidyshare_v" + version + "_" + settings["GOOS"] + "_" + settings["GOARCH"]
	if filepath.Base(binary) != name {
		return errors.New("binary filename does not match its platform metadata")
	}
	if revision := settings["vcs.revision"]; revision != "" && revision != commit {
		return errors.New("binary VCS commit differs from prepared source")
	}
	receipt := binaryReceipt{Name: name, Version: "v" + version, Commit: commit, GoVersion: info.GoVersion, GOOS: settings["GOOS"], GOARCH: settings["GOARCH"], CGOEnabled: settings["CGO_ENABLED"], Compiler: settings["-compiler"], LDFlags: ldflags, VCSRevision: settings["vcs.revision"], VCSModified: settings["vcs.modified"]}
	if native {
		if receipt.GOOS != runtime.GOOS || receipt.GOARCH != runtime.GOARCH {
			return errors.New("native version smoke requires the matching host platform")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		output := &nativeVersionBuffer{cancel: cancel}
		command := exec.CommandContext(ctx, binary, "version")
		command.Stdout = output
		command.Stderr = output
		command.WaitDelay = time.Second
		if err := command.Run(); err != nil {
			return fmt.Errorf("native version smoke: %w", err)
		}
		if output.exceeded || strings.TrimSpace(string(output.data)) != releaseVersionOutput(version, commit) {
			return errors.New("native binary version or commit mismatch")
		}
		receipt.NativeVersion = strings.TrimSpace(string(output.data))
	}
	receipt.SHA256, err = hashReleaseFile(binary)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(destination), ".binary-receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}

func readBinaryReceipt(path string) (binaryReceipt, error) {
	var receipt binaryReceipt
	stat, err := os.Lstat(path)
	if err != nil {
		return receipt, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 64<<10 {
		return receipt, errors.New("receipt must be a bounded plain file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return receipt, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return receipt, errors.New("receipt contains trailing data")
	}
	return receipt, nil
}

func verifyGoReleaserMetadata(path, version, commit string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var metadata struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return err
	}
	if metadata.Version != version || metadata.Commit != commit {
		return errors.New("GoReleaser version or source commit mismatch")
	}
	return nil
}

func hashReleaseFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Metadata checks do not execute foreign binaries.
func verifyReleaseAssets(directory, version, commit string) error {
	if err := validateBundleIdentity(version, commit); err != nil {
		return err
	}
	prefix := "tidyshare_v" + version
	checksumName := prefix + "_checksums.txt"
	archiveName := prefix + "_agent-bundle.tar.gz"
	checksums := map[string]string{}
	expected := map[string]bool{archiveName: true}
	goVersion := ""
	for _, target := range []struct{ os, arch, cgo string }{{"linux", "amd64", "0"}, {"linux", "arm64", "0"}, {"darwin", "amd64", "1"}, {"darwin", "arm64", "1"}} {
		name := prefix + "_" + target.os + "_" + target.arch
		expected[name] = true
		path := filepath.Join(directory, name)
		stat, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("release asset is not a plain file: %s", name)
		}
		info, settings, err := readBinarySettings(path)
		if err != nil {
			return fmt.Errorf("read %s metadata: %w", name, err)
		}
		if info.GoVersion == "" {
			return fmt.Errorf("missing Go version in %s", name)
		}
		if goVersion == "" {
			goVersion = info.GoVersion
		} else if goVersion != info.GoVersion {
			return errors.New("binary Go versions differ")
		}
		if settings["GOOS"] != target.os || settings["GOARCH"] != target.arch || settings["CGO_ENABLED"] != target.cgo {
			return fmt.Errorf("platform metadata mismatch: %s", name)
		}
		// -trimpath removes ldflags from buildinfo. Check the digest-bound job receipt.
		receipt, err := readBinaryReceipt(filepath.Join(directory, "binary-"+target.os+"-"+target.arch+".json"))
		if err != nil {
			return err
		}
		hash, err := hashReleaseFile(path)
		if err != nil {
			return err
		}
		if receipt.Name != name || receipt.SHA256 != hash || receipt.Version != "v"+version || receipt.Commit != commit || receipt.LDFlags != releaseLDFlags(version, commit) {
			return fmt.Errorf("binary receipt identity mismatch: %s", name)
		}
		if receipt.GoVersion != info.GoVersion || receipt.GOOS != target.os || receipt.GOARCH != target.arch || receipt.CGOEnabled != target.cgo || receipt.Compiler != settings["-compiler"] || receipt.Compiler != "gc" || receipt.VCSRevision != settings["vcs.revision"] || receipt.VCSModified != settings["vcs.modified"] {
			return fmt.Errorf("binary receipt metadata mismatch: %s", name)
		}
		if receipt.VCSRevision != "" && receipt.VCSRevision != commit {
			return fmt.Errorf("binary VCS source mismatch: %s", name)
		}
		if receipt.NativeVersion != "" && receipt.NativeVersion != releaseVersionOutput(version, commit) {
			return fmt.Errorf("native smoke receipt mismatch: %s", name)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "tidyshare_v") && !expected[entry.Name()] && entry.Name() != checksumName {
			return fmt.Errorf("unexpected release asset %s", entry.Name())
		}
	}
	for name := range expected {
		path := filepath.Join(directory, name)
		stat, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("release asset is not a plain file: %s", name)
		}
		checksums[name], err = hashReleaseFile(path)
		if err != nil {
			return err
		}
	}
	if err := verifyBundle(filepath.Join(directory, archiveName), version, commit); err != nil {
		return err
	}
	checksumPath := filepath.Join(directory, checksumName)
	stat, err := os.Lstat(checksumPath)
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return errors.New("checksum file is not a plain file")
	}
	file, err := os.Open(checksumPath)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	seen := map[string]bool{}
	for scanner.Scan() {
		hash, name, ok := strings.Cut(scanner.Text(), "  ")
		if !ok || !expected[name] || seen[name] || hash != checksums[name] {
			return errors.New("release checksum entry is invalid, duplicate, or includes the checksum file")
		}
		seen[name] = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(seen) != 5 {
		return errors.New("checksums must cover exactly four binaries and one bundle")
	}
	return nil
}

func run() error {
	flags := flag.NewFlagSet("agent-bundle", flag.ContinueOnError)
	source := flags.String("source", "agent-bundle", "Canonical payload directory")
	out := flags.String("out", ".artifacts/release", "Archive output directory")
	version := flags.String("version", "", "Release version without a leading v")
	commit := flags.String("commit", "", "Full source commit")
	verify := flags.String("verify", "", "Check an archive instead of building it")
	assets := flags.String("verify-assets", "", "Check four binary metadata records, the bundle, and common checksums")
	record := flags.String("record-binary", "", "Record one binary build receipt")
	receipt := flags.String("receipt", "", "Build receipt output path")
	ldflags := flags.String("build-ldflags", "", "Exact prepared linker flags used by the build job")
	native := flags.Bool("native", false, "Execute the native binary version command for this receipt")
	metadata := flags.String("goreleaser-metadata", "", "Check GoReleaser metadata before recording a binary")
	printLDFlags := flags.Bool("print-ldflags", false, "Print the canonical linker inputs for the version and commit")
	refType := flags.String("ref-type", "", "Print the canonical version for tag or branch")
	ref := flags.String("ref", "", "Original Git ref name")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	if *printLDFlags {
		if *refType != "" || *verify != "" || *assets != "" || *record != "" || *native || *receipt != "" || *ldflags != "" || *metadata != "" {
			return errors.New("select one operation")
		}
		if err := validateBundleIdentity(*version, *commit); err != nil {
			return err
		}
		fmt.Println(releaseLDFlags(*version, *commit))
		return nil
	}
	if *refType != "" {
		if *verify != "" || *assets != "" || *record != "" {
			return errors.New("select one operation")
		}
		normalized, err := releaseVersion(*refType, *ref, *commit)
		if err != nil {
			return err
		}
		fmt.Println(normalized)
		return nil
	}
	if *record != "" {
		if *verify != "" || *assets != "" || *receipt == "" {
			return errors.New("select one operation and provide a receipt path")
		}
		if *metadata != "" {
			if err := verifyGoReleaserMetadata(*metadata, *version, *commit); err != nil {
				return err
			}
		}
		return recordBinaryReceipt(*record, *receipt, *version, *commit, *ldflags, *native)
	}
	if *native || *receipt != "" || *ldflags != "" || *metadata != "" {
		return errors.New("binary receipt options require record-binary")
	}
	if *assets != "" {
		if *verify != "" {
			return errors.New("select one operation")
		}
		return verifyReleaseAssets(*assets, *version, *commit)
	}
	if *verify != "" {
		return verifyBundle(*verify, *version, *commit)
	}
	archive, err := buildBundle(*source, *out, *version, *commit)
	if err != nil {
		return err
	}
	fmt.Println(archive)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agent bundle:", err)
		os.Exit(1)
	}
}
