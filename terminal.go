package main

import (
	"os"
	"syscall"
	"unsafe"
)

// interactive reports whether stdin and stdout are both a terminal, so an
// agent started in place has someone to talk to.
func interactive() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), ioctlGetTermios, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
