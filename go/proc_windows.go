//go:build windows

package main

import "syscall"

func sysProcAttrDetach() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008} // DETACHED_PROCESS
}
