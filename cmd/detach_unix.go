//go:build !windows

package cmd

import "syscall"

// detachedSysProcAttr puts the worker in its own session so it survives the
// launching terminal.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
