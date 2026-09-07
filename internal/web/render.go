package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
)

type Renderer struct {
	pages    *template.Template
	base     *template.Template
	fragment *template.Template
}

func NewRenderer() (*Renderer, error) {
	funcs := template.FuncMap{
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of args")
			}
			out := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: non-string key %v", values[i])
				}
				out[key] = values[i+1]
			}
			return out, nil
		},
	}
	pages, err := template.New("").Funcs(funcs).ParseFS(Pages(),
		"apps.html", "app_detail.html", "deploy.html", "settings.html",
		"login.html", "setup.html", "files.html")
	if err != nil {
		return nil, err
	}
	base, err := template.New("base.html").Funcs(funcs).ParseFS(Pages(), "base.html")
	if err != nil {
		return nil, err
	}
	fragment, err := template.New("files.html").Funcs(funcs).ParseFS(Pages(), "files.html")
	if err != nil {
		return nil, err
	}
	return &Renderer{pages: pages, base: base, fragment: fragment}, nil
}

type layoutData struct {
	Title  string
	Body   template.HTML
	Authed bool
	Error  string
}

func (r *Renderer) Render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	bodyBuf := &bytes.Buffer{}
	if err := r.pages.ExecuteTemplate(bodyBuf, page, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	authed, errMsg := pageMeta(data)
	if err := r.base.Execute(w, layoutData{
		Body:   template.HTML(bodyBuf.String()),
		Authed: authed,
		Error:  errMsg,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// RenderFragment writes a stand-alone HTML fragment (no base layout) and
// is used for partials fetched over fetch() / HTMX.
func (r *Renderer) RenderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.fragment.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func pageMeta(data any) (bool, string) {
	type carrier interface {
		AuthedOK() bool
		ErrorStr() string
	}
	if c, ok := data.(carrier); ok {
		return c.AuthedOK(), c.ErrorStr()
	}
	return false, ""
}

// PageData is the common context for page templates.
type PageData struct {
	Authed bool
	Error  string
}

func (p PageData) AuthedOK() bool   { return p.Authed }
func (p PageData) ErrorStr() string { return p.Error }
