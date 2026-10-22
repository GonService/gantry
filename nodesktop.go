//go:build !desktop

package main

import "io/fs"

const desktopBuild = false

var desktopOrigins []string

func runDesktop(fs.FS, string, []string, string, string, func() string) error { return nil }
