package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/store"
)

const (
	filesMaxDepth     = 6
	filesMaxEntries   = 2000
	filesViewMaxBytes = 256 * 1024
)

type fileNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	IsDir    bool        `json:"isDir"`
	Size     int64       `json:"size,omitempty"`
	Children []*fileNode `json:"children,omitempty"`
}

type filesFragmentData struct {
	App   *store.App
	Tree  *fileNode
	Error string
}

// handleAppFiles serves the source-tree viewer for an app. Always renders
// the files.html fragment; the page is included on app_detail via a
// separate load.
func (s *Server) handleAppFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	app, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	sourceDir, err := store.AppSourceDir(s.Cfg.AppsRoot, app)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := filesFragmentData{App: app}
	if _, err := os.Stat(sourceDir); err != nil {
		data.Error = "source directory not found: " + sourceDir
		s.Renderer.RenderFragment(w, "files.html", data)
		return
	}
	tree, err := buildFileTree(sourceDir, filesMaxDepth, filesMaxEntries)
	if err != nil {
		data.Error = err.Error()
	} else {
		data.Tree = tree
	}
	s.Renderer.RenderFragment(w, "files.html", data)
}

// handleAppFileView renders the contents of a single file from the app's
// source directory. Refuses any path that escapes the source dir.
func (s *Server) handleAppFileView(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	app, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	sourceDir, err := store.AppSourceDir(s.Cfg.AppsRoot, app)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(filepath.Join(sourceDir, rel))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(abs, sourceDir+string(filepath.Separator)) && abs != sourceDir {
		http.Error(w, "path outside source directory", http.StatusForbidden)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "is a directory", http.StatusBadRequest)
		return
	}
	if info.Size() > filesViewMaxBytes {
		http.Error(w, fmt.Sprintf("file too large (%d bytes; max %d)", info.Size(), filesViewMaxBytes), http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeFile(w, r, abs)
}

// buildFileTree walks rootDir up to maxDepth and returns the tree of regular
// files and directories. Hidden entries (starting with ".") are skipped.
// Aborts if the entry count exceeds maxEntries to avoid runaway responses
// on accidentally-large trees.
func buildFileTree(rootDir string, maxDepth, maxEntries int) (*fileNode, error) {
	count := 0
	var walk func(path string, depth int) (*fileNode, error)
	walk = func(path string, depth int) (*fileNode, error) {
		if count >= maxEntries {
			return nil, fmt.Errorf("too many entries (>%d)", maxEntries)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(rootDir, path)
		if rel == "." {
			rel = ""
		}
		node := &fileNode{
			Name:  info.Name(),
			Path:  rel,
			IsDir: info.IsDir(),
			Size:  info.Size(),
		}
		if !info.IsDir() {
			count++
			return node, nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return entries[i].Name() < entries[j].Name()
		})
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if depth >= maxDepth {
				break
			}
			child, err := walk(filepath.Join(path, e.Name()), depth+1)
			if err != nil {
				return nil, err
			}
			node.Children = append(node.Children, child)
		}
		return node, nil
	}
	return walk(rootDir, 0)
}
