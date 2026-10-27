package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type pathFlags []string

func (p *pathFlags) String() string         { return strings.Join(*p, ",") }
func (p *pathFlags) Set(value string) error { *p = append(*p, value); return nil }

type storagePolicy struct {
	Profile   string   `json:"profile,omitempty"`
	Protected []string `json:"protected_roots,omitempty"`
}

var workstationProtectedSuffixes = []string{"OrbStack", "Library/Group Containers", "Library/Keychains", "Library/Application Support", "Library/Containers", "Library/Safari", "Pictures", ".config", ".mozilla", ".thunderbird", ".ssh", ".gnupg", ".docker", ".codex", ".claude", ".local/share/agent-compat", ".ollama", ".lmstudio", ".cache/huggingface"}

func policyFlags(f *flag.FlagSet) (*string, *pathFlags) {
	profile := f.String("profile", "", "storage policy: workstation or empty")
	var protected pathFlags
	f.Var(&protected, "protect", "protected absolute path; repeat for multiple roots")
	return profile, &protected
}

func newStoragePolicy(profile string, protected []string) (storagePolicy, error) {
	p := storagePolicy{Profile: profile, Protected: append([]string{}, protected...)}
	if profile != "" && profile != "workstation" {
		return p, errors.New("unknown storage profile")
	}
	if profile == "workstation" {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		for _, suffix := range workstationProtectedSuffixes {
			p.Protected = append(p.Protected, filepath.Join(home, suffix))
		}
	}
	for _, path := range p.Protected {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return p, errors.New("protected roots must be clean absolute paths")
		}
	}
	sort.Strings(p.Protected)
	return p, nil
}

func (p storagePolicy) excludes(path string) bool {
	for _, root := range p.Protected {
		if protectedWithin(root, path) {
			return true
		}
	}
	return false
}

func (p storagePolicy) overlaps(path string) bool {
	for _, root := range p.Protected {
		if protectedWithin(root, path) || protectedWithin(path, root) {
			return true
		}
	}
	return false
}

func privateTemp(prefix string) (string, error) {
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(root, prefix)
}

// Darwin denial checks also cover case and canonical Unicode aliases.
// Allow-root checks remain literal, so case-sensitive volumes cannot expand scope.
func protectedWithin(root, path string) bool {
	if runtime.GOOS == "darwin" {
		root = norm.NFD.String(cases.Fold().String(root))
		path = norm.NFD.String(cases.Fold().String(path))
	}
	return cacheWithin(root, path)
}
