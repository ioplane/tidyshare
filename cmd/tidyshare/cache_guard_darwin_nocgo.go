//go:build darwin && !cgo

package main

import (
	"context"
	"errors"
)

func cacheBusy(context.Context, string, []uintptr) error {
	return errors.New("Darwin cache cleanup requires a build with CGo and libproc")
}

func cacheMountGuard(string) error {
	return errors.New("Darwin cache cleanup requires a build with CGo and libproc")
}
