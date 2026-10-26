package main

import (
	"embed"
	"io/fs"
)

var Version = "1.0.24"

var BuildTime = "dev"

//go:embed web
var webFS embed.FS

func webSub() (fs.FS, error) { return fs.Sub(webFS, "web") }
