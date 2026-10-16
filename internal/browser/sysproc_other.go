//go:build !linux

package browser

import "syscall"

// sysProcAttr starts the browser in its own process group. Only Linux has a
// parent-death signal; elsewhere Close and the ctx kill are the only cleanup.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
