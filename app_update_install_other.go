//go:build !darwin

package main

func appUpdateDirectoryWritable(string) bool { return false }
