//go:build ignore

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var fixtureAssetNames = []string{"tidyshare_v0.1.0_linux_amd64", "tidyshare_v0.1.0_linux_arm64", "tidyshare_v0.1.0_darwin_amd64", "tidyshare_v0.1.0_darwin_arm64", "tidyshare_v0.1.0_agent-bundle.tar.gz"}

func tinyReleaseBinary(t *testing.T, destination, targetOS, targetArch, cgo, version, commit string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"fmt\"\nvar version=\"dev\"\nvar commit=\"\"\nfunc main(){fmt.Printf(\"tidyshare %s (%s)\\n\",version,commit)}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-trimpath", "-ldflags", fmt.Sprintf("-s -w -X main.version=v%s -X main.commit=%s", version, commit), "-o", destination, source)
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOOS="+targetOS, "GOARCH="+targetArch, "CGO_ENABLED="+cgo)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("tiny binary build: %v %s", err, output)
	}
}

func fixtureChecksums(t *testing.T, directory string) {
	t.Helper()
	var lines strings.Builder
	for _, name := range fixtureAssetNames {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&lines, "%x  %s\n", sha256.Sum256(data), name)
	}
	if err := os.WriteFile(filepath.Join(directory, "tidyshare_v0.1.0_checksums.txt"), []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func releaseAssetsFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	for _, target := range []struct{ os, arch, cgo string }{{"linux", "amd64", "0"}, {"linux", "arm64", "0"}, {"darwin", "amd64", "1"}, {"darwin", "arm64", "1"}} {
		binary := filepath.Join(directory, "tidyshare_v0.1.0_"+target.os+"_"+target.arch)
		tinyReleaseBinary(t, binary, target.os, target.arch, target.cgo, "0.1.0", fixtureCommit)
		if err := recordBinaryReceipt(binary, filepath.Join(directory, "binary-"+target.os+"-"+target.arch+".json"), "0.1.0", fixtureCommit, "-s -w -X main.version=v0.1.0 -X main.commit="+fixtureCommit, runtime.GOOS == target.os && runtime.GOARCH == target.arch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buildBundle(bundleSource(t), directory, "0.1.0", fixtureCommit); err != nil {
		t.Fatal(err)
	}
	fixtureChecksums(t, directory)
	return directory
}

func TestReleaseAssetsRequireFourMatchedBinariesAndBundle(t *testing.T) {
	original := releaseAssetsFixture(t)
	if err := verifyReleaseAssets(original, "0.1.0", fixtureCommit); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"version", "commit", "architecture", "cgo", "missing", "missing-receipt", "receipt-sha", "checksum", "self-checksum", "extra"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			entries, err := os.ReadDir(original)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(original, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, entry.Name()), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(directory, "tidyshare_v0.1.0_linux_amd64")
			receiptPath := filepath.Join(directory, "binary-linux-amd64.json")
			mutateReceipt := func(change func(*binaryReceipt)) {
				data, err := os.ReadFile(receiptPath)
				if err != nil {
					t.Fatal(err)
				}
				var receipt binaryReceipt
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				change(&receipt)
				data, err = json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(receiptPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "version":
				mutateReceipt(func(receipt *binaryReceipt) { receipt.Version = "v0.2.0" })
			case "commit":
				mutateReceipt(func(receipt *binaryReceipt) { receipt.Commit = strings.Repeat("a", 40) })
			case "architecture":
				tinyReleaseBinary(t, binary, "linux", "arm64", "0", "0.1.0", fixtureCommit)
				fixtureChecksums(t, directory)
			case "cgo":
				tinyReleaseBinary(t, binary, "linux", "amd64", "1", "0.1.0", fixtureCommit)
				fixtureChecksums(t, directory)
			case "missing":
				_ = os.Remove(binary)
			case "missing-receipt":
				_ = os.Remove(receiptPath)
			case "receipt-sha":
				mutateReceipt(func(receipt *binaryReceipt) { receipt.SHA256 = strings.Repeat("a", 64) })
			case "checksum":
				_ = os.WriteFile(binary, []byte("corrupt"), 0o600)
			case "self-checksum":
				file, err := os.OpenFile(filepath.Join(directory, "tidyshare_v0.1.0_checksums.txt"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = fmt.Fprintf(file, "%s  tidyshare_v0.1.0_checksums.txt\n", strings.Repeat("0", 64))
				_ = file.Close()
			case "extra":
				_ = os.WriteFile(filepath.Join(directory, "tidyshare_v0.0.0_stale"), []byte("stale"), 0o600)
			}
			if err := verifyReleaseAssets(directory, "0.1.0", fixtureCommit); err == nil {
				t.Fatal("incomplete or mismatched release accepted")
			}
		})
	}
}

func TestNativeReceiptRejectsWrongRuntimeIdentity(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("release platforms are Linux and Darwin")
	}
	for _, kind := range []string{"version", "commit"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			binary := filepath.Join(directory, "tidyshare_v0.1.0_"+runtime.GOOS+"_"+runtime.GOARCH)
			version, commit := "0.1.0", fixtureCommit
			if kind == "version" {
				version = "0.2.0"
			} else {
				commit = strings.Repeat("a", 40)
			}
			cgo := "0"
			if runtime.GOOS == "darwin" {
				cgo = "1"
			}
			tinyReleaseBinary(t, binary, runtime.GOOS, runtime.GOARCH, cgo, version, commit)
			receipt := filepath.Join(directory, "must-not-publish.json")
			if err := recordBinaryReceipt(binary, receipt, "0.1.0", fixtureCommit, "-s -w -X main.version=v0.1.0 -X main.commit="+fixtureCommit, true); err == nil {
				t.Fatal("wrong native runtime identity accepted")
			}
			if _, err := os.Stat(receipt); !os.IsNotExist(err) {
				t.Fatal("failed native identity published a receipt")
			}
		})
	}
}

const fixtureCommit = "0123456789abcdef0123456789abcdef01234567"

func TestPreparedLinkerInputsCLI(t *testing.T) {
	command := exec.Command("go", "run", "agent_bundle.go", "-print-ldflags", "-version", "0.1.0", "-commit", fixtureCommit)
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "-s -w -X main.version=v0.1.0 -X main.commit=0123456789abcdef0123456789abcdef01234567\n" {
		t.Fatalf("prepared linker output: %q %v", output, err)
	}
}

func bundleSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	names := []string{"SKILL.md", "README.md", "LICENSE", "NOTICE", "workflows/inventory-review.md", "workflows/managed-cleanup.md", "workflows/recovery-diagnostics.md", "workflows/adapter-development.md", "workflows/testing.md", "workflows/release.md", "examples/cli.md", "examples/codex-mcp.json", "examples/claude-code-mcp.json"}
	for _, name := range names {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture: "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestReleaseVersionNormalization(t *testing.T) {
	for _, tc := range []struct {
		kind, ref, want string
		bad             bool
	}{
		{"tag", "v0.1.0", "0.1.0", false},
		{"tag", "v1.2.3-RC.1", "1.2.3-RC.1", false},
		{"branch", "main", "0.0.0-dev." + fixtureCommit, false},
		{"tag", "v01.2.3", "", true},
		{"tag", "1.2.3", "", true},
		{"tag", "v1.2.3-01", "", true},
		{"tag", "v1.2.3-..", "", true},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			got, err := releaseVersion(tc.kind, tc.ref, fixtureCommit)
			if tc.bad {
				if err == nil {
					t.Fatal("invalid tag accepted")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q %v want %q", got, err, tc.want)
			}
		})
	}
	if _, err := releaseVersion("branch", "main", ""); err == nil {
		t.Fatal("missing commit accepted")
	}
}

func TestSnapshotPreservesFullNumericSourceSHA(t *testing.T) {
	commit := strings.Repeat("0", 39) + "1"
	got, err := releaseVersion("branch", "main", commit)
	if err != nil || got != "0.0.0-dev."+commit {
		t.Fatalf("snapshot must preserve any full source SHA: %q %v", got, err)
	}
}

func TestBundleDeterministicAndVersionMatched(t *testing.T) {
	source := bundleSource(t)
	first, err := buildBundle(source, t.TempDir(), "0.1.0", fixtureCommit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildBundle(source, t.TempDir(), "0.1.0", fixtureCommit)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first) != "tidyshare_v0.1.0_agent-bundle.tar.gz" {
		t.Fatal("incorrect version prefix")
	}
	one, _ := os.ReadFile(first)
	two, _ := os.ReadFile(second)
	if !bytes.Equal(one, two) {
		t.Fatal("same inputs produced different archives")
	}
	if err := verifyBundle(first, "0.1.0", fixtureCommit); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ version, commit string }{{"0.2.0", fixtureCommit}, {"0.1.0", strings.Repeat("a", 40)}, {"0.1.0", ""}} {
		if err := verifyBundle(first, tc.version, tc.commit); err == nil {
			t.Fatal("mismatch accepted")
		}
	}
	entries := readArchive(t, first)
	if len(entries) != 16 {
		t.Fatalf("archive entries=%d want root plus15files", len(entries))
	}
	var manifest map[string]any
	for _, entry := range entries {
		if entry.header.Name == "tidyshare-agent-bundle/VERSION" && string(entry.body) != "v0.1.0\n" {
			t.Fatal("VERSION differs from embedded version")
		}
		if entry.header.Name == "tidyshare-agent-bundle/manifest.json" {
			if err := json.Unmarshal(entry.body, &manifest); err != nil {
				t.Fatal(err)
			}
		}
		if entry.header.Typeflag == tar.TypeReg && (entry.header.Mode != 0o644 || entry.header.Uid != 0 || entry.header.Gid != 0 || entry.header.ModTime.Unix() != 0) {
			t.Fatalf("nondeterministic header: %+v", entry.header)
		}
	}
	if len(manifest) != 3 || manifest["version"] != "v0.1.0" || manifest["commit"] != fixtureCommit {
		t.Fatalf("manifest contract: %+v", manifest)
	}
	files, ok := manifest["files"].(map[string]any)
	if !ok || len(files) != 14 || files["VERSION"] == nil || files["manifest.json"] != nil {
		t.Fatal("manifest payload coverage incorrect")
	}
}

func TestBundleRejectsIncompleteOrUnsafeSource(t *testing.T) {
	for _, kind := range []string{"missing-workflow", "symlink", "unexpected", "executable"} {
		t.Run(kind, func(t *testing.T) {
			source := bundleSource(t)
			path := filepath.Join(source, "workflows/testing.md")
			switch kind {
			case "missing-workflow":
				_ = os.Remove(path)
			case "symlink":
				_ = os.Remove(path)
				if err := os.Symlink("../README.md", path); err != nil {
					t.Fatal(err)
				}
			case "unexpected":
				_ = os.WriteFile(filepath.Join(source, "VERSION"), []byte("v0.0.0"), 0o600)
			case "executable":
				_ = os.Chmod(path, 0o755)
			}
			if _, err := buildBundle(source, t.TempDir(), "0.1.0", fixtureCommit); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
	for _, version := range []string{"v0.1.0", "../escape", "0.1.0/evil"} {
		if _, err := buildBundle(bundleSource(t), t.TempDir(), version, fixtureCommit); err == nil {
			t.Fatal("invalid raw version accepted")
		}
	}
}

type archiveEntry struct {
	header tar.Header
	body   []byte
}

func readArchive(t *testing.T, path string) []archiveEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var entries []archiveEntry
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, archiveEntry{*header, body})
	}
	return entries
}

func tamperedArchive(t *testing.T, entries []archiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tampered.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		entry.header.Size = int64(len(entry.body))
		if err := tw.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBundleRejectsCorruptOrUnsafeArchives(t *testing.T) {
	original, err := buildBundle(bundleSource(t), t.TempDir(), "0.1.0", fixtureCommit)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"checksum", "missing-workflow", "duplicate", "traversal", "symlink", "absolute", "executable"} {
		t.Run(kind, func(t *testing.T) {
			entries := readArchive(t, original)
			switch kind {
			case "checksum":
				for i := range entries {
					if strings.HasSuffix(entries[i].header.Name, "/README.md") {
						entries[i].body = []byte("corrupt")
					}
				}
			case "missing-workflow":
				for i := range entries {
					if strings.HasSuffix(entries[i].header.Name, "/workflows/testing.md") {
						entries = append(entries[:i], entries[i+1:]...)
						break
					}
				}
			case "duplicate":
				entries = append(entries, entries[1])
			case "traversal":
				entries[1].header.Name = "tidyshare-agent-bundle/../escape"
			case "absolute":
				entries[1].header.Name = "/escape"
			case "symlink":
				entries[1].header.Typeflag = tar.TypeSymlink
				entries[1].header.Linkname = "../escape"
				entries[1].body = nil
			case "executable":
				entries[1].header.Mode = 0o755
			}
			if err := verifyBundle(tamperedArchive(t, entries), "0.1.0", fixtureCommit); err == nil {
				t.Fatal("corrupt archive accepted")
			}
		})
	}
}
