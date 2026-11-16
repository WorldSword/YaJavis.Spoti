//go:build !linux

package main

import "syscall"

func pluginProcAttr() *syscall.SysProcAttr { return nil }
