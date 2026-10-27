package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rejecting whitespace or forwarding duplicates breaks CLI/MCP format parity.
func TestMCPFormatsCanonicalForwarding(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	ops, err := newMCPOperations(mcpOptions{root: root, out: out, imageOwner: "root"})
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "arguments")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TIDYSHARE_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ops.executable = script
	for _, tc := range []struct {
		input, want string
		invalid     bool
	}{
		{"", "json,jsonl,md", false},
		{" json,md,jsonl,json ", "json,md,jsonl", false},
		{"xml", "", true},
		{"json,", "", true},
		{"json,,md", "", true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "args")
			t.Setenv("TIDYSHARE_ARGS", capture)
			_, err := ops.runIn(context.Background(), out, "scan", false, tc.input)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid format accepted")
				}
				if _, e := os.Stat(capture); !os.IsNotExist(e) {
					t.Fatal("invalid format ran executable")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(string(args), "-formats\n"+tc.want+"\n") {
				t.Fatalf("noncanonical arguments: %s", args)
			}
		})
	}
}

// Returning url.Parse's raw cause exposes userinfo before credential validation.
func TestPodmanMalformedEndpointDoesNotEchoUserinfo(t *testing.T) {
	const marker = "synthetic-userinfo-marker"
	for _, endpoint := range []string{"unix://" + marker + ":bad%zz@/tmp/podman.sock", "unix://" + marker + "@/%zz"} {
		_, err := socketPath(endpoint)
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("unsafe diagnostic: %v", err)
		}
		_, err = newMCPOperations(mcpOptions{root: t.TempDir(), out: t.TempDir(), imageSocket: endpoint})
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("unsafe MCP startup diagnostic: %v", err)
		}
	}
}
