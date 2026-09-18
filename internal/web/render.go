package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"net/http"
)

type Renderer struct {
	pages    *template.Template
	base     *template.Template
	fragment *template.Template
}

// assetVersions maps a static asset path (e.g. "/static/js/app.js") to
// a content hash query (?v=…), computed once from the embedded files at
// startup. Templates render versioned URLs so browsers hard-cache the
// assets and any change to them busts every client's cache
// automatically — without this, a browser that cached an old app.js
// keeps running old UI logic against new templates.
var assetVersions = map[string]string{}

func computeAssetVersions() {
	for _, p := range []string{"css/app.css", "js/app.js"} {
		f, err := Static().Open(p)
		if err != nil {
			continue
		}
		h := sha256.New()
		_, _ = io.Copy(h, f)
		f.Close()
		assetVersions["/static/"+p] = hex.EncodeToString(h.Sum(nil))[:10]
	}
}

// assetURL renders a cache-busted URL for a static asset.
func assetURL(path string) string {
	if v, ok := assetVersions[path]; ok {
		return path + "?v=" + v
	}
	return path
}

func NewRenderer() (*Renderer, error) {
	computeAssetVersions()
	funcs := template.FuncMap{
		"assetURL": assetURL,
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
		"login.html", "setup.html", "files.html", "icons.html")
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
	Title     string
	Body      template.HTML
	Authed    bool
	CSRFToken string
	Error     string
	FlashKind string
}

// Render executes a page inside the base layout. The CSRF token comes
// from the request context (set by RequireAuth) so every form rendered
// on an authed page carries it.
func (r *Renderer) Render(w http.ResponseWriter, req *http.Request, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	bodyBuf := &bytes.Buffer{}
	if err := r.pages.ExecuteTemplate(bodyBuf, page, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	authed, errMsg, title := pageMeta(data)
	csrf, _ := req.Context().Value(ctxCSRF).(string)
	if err := r.base.Execute(w, layoutData{
		Title:     title,
		Body:      template.HTML(bodyBuf.String()),
		Authed:    authed,
		CSRFToken: csrf,
		Error:     errMsg,
		FlashKind: flashKind(data),
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

func pageMeta(data any) (bool, string, string) {
	type carrier interface {
		AuthedOK() bool
		ErrorStr() string
		TitleStr() string
	}
	if c, ok := data.(carrier); ok {
		return c.AuthedOK(), c.ErrorStr(), c.TitleStr()
	}
	return false, "", ""
}

func flashKind(data any) string {
	if c, ok := data.(interface{ FlashKindStr() string }); ok {
		return c.FlashKindStr()
	}
	return ""
}

// PageData is the common context for page templates.
type PageData struct {
	Authed    bool
	Title     string
	CSRFToken string
	Error     string
	FlashKind string
}

func (p PageData) AuthedOK() bool       { return p.Authed }
func (p PageData) ErrorStr() string     { return p.Error }
func (p PageData) TitleStr() string     { return p.Title }
func (p PageData) FlashKindStr() string { return p.FlashKind }

// pageCtx builds the common PageData for an authed page, including the
// CSRF token from the request context so page-level forms carry it.
// (The layout nav gets the token via $.CSRFToken; page templates read
// it from their own data.) A one-shot flash left by a previous handler
// (see flash.go) is folded into Error/FlashKind so the layout renders it.
func pageCtx(r *http.Request, title string) PageData {
	csrf, _ := r.Context().Value(ctxCSRF).(string)
	f := flashFrom(r)
	return PageData{Authed: true, Title: title, CSRFToken: csrf, Error: f.Text, FlashKind: f.Kind}
}
