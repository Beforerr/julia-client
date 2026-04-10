//go:build !windows

package main

import "syscall"

func sysProcAttrDetach() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
