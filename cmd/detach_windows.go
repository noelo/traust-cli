//go:build windows

package cmd

import "syscall"

// detachedSysProcAttr creates a new process group on Windows; there is no
// session concept equivalent to setsid, but detaching from the console
// process group keeps the worker alive past the launcher.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
