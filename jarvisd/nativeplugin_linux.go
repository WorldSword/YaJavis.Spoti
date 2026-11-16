//go:build linux

package main

import "syscall"

// pluginProcAttr — плагин умирает вместе с jarvisd (не остаётся сиротой после обновления или падения).
func pluginProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }
