//go:build darwin && !cgo

package main

import "errors"

func managedProcessCapability() error {
	return errors.New("native process ownership requires cgo on Darwin")
}
func waitManagedLeader(pid int) (bool, error) { return false, managedProcessCapability() }
