package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed pages/* static/*
var content embed.FS

func Pages() fs.FS {
	sub, _ := fs.Sub(content, "pages")
	return sub
}

func Static() http.FileSystem {
	sub, _ := fs.Sub(content, "static")
	return http.FS(sub)
}