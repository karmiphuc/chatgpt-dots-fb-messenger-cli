//go:build linux

package browser

import "syscall"

// sysProcAttr starts the browser in its own process group, and has the kernel
// SIGKILL it if fbm dies without running Close.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
