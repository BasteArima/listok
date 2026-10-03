package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/BasteArima/listok/internal/store"
)

// page — данные для шаблона страницы. Data — данные конкретной страницы.
type page struct {
	Title string
	User  *store.User
	Error string
	Form  map[string]string
	Data  any
}

type pageTemplate struct {
	t *template.Template
}

// loadPages собирает каждую страницу отдельно: layout + pages/<имя>.html.
// Так блоки "content" разных страниц не перезаписывают друг друга.
func loadPages() (map[string]*pageTemplate, error) {
	files, err := fs.Glob(assets, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	out := map[string]*pageTemplate{}
	for _, f := range files {
		name := strings.TrimSuffix(path.Base(f), ".html")
		t, err := template.New(name).ParseFS(assets, "templates/layout.html", f)
		if err != nil {
			return nil, err
		}
		out[name] = &pageTemplate{t: t}
	}
	return out, nil
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	if p.User == nil {
		p.User = userFrom(r.Context())
	}
	if p.Form == nil {
		p.Form = map[string]string{}
	}
	pt, ok := s.pages[name]
	if !ok {
		s.Log.Error("нет шаблона страницы", "page", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Рендерим в буфер: ошибка шаблона не оставит полстраницы с кодом 200.
	var buf bytes.Buffer
	if err := pt.t.ExecuteTemplate(&buf, "layout", p); err != nil {
		s.Log.Error("ошибка шаблона", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}
