//go:build darwin

package main

import "golang.org/x/sys/unix"

func appUpdateDirectoryWritable(dir string) bool { return unix.Access(dir, unix.W_OK) == nil }
