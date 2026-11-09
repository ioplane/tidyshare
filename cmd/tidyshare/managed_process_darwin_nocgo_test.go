//go:build darwin && !cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedDarwinNoCgoRefusesBeforeStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-start")
	_, err := managedCommand(context.Background(), "/bin/sh", []string{"-c", `touch "$1"`, "fixture", marker}, os.Environ(), "/")
	if err == nil || !strings.Contains(err.Error(), "requires cgo") {
		t.Fatalf("missing capability refusal: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported native command started")
	}
}
