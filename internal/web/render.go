package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/BasteArima/listok/internal/entry"
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

type statsData struct {
	L   store.List
	OOB bool
}

var funcs = template.FuncMap{
	"kindLabel": func(k string) string {
		switch k {
		case "domain":
			return "домен"
		case "cidr4":
			return "IPv4"
		case "cidr6":
			return "IPv6"
		}
		return k
	},
	"roleLabel": func(r string) string {
		switch r {
		case "owner":
			return "владелец"
		case "editor":
			return "редактор"
		case "viewer":
			return "просмотр"
		case "admin":
			return "админ"
		}
		return r
	},
	"statusLabel": func(s string) string {
		switch s {
		case "added":
			return "добавлено"
		case "exists":
			return "уже есть"
		case "covered":
			return "уже покрыто"
		case "error":
			return "ошибка"
		}
		return s
	},
	"warnLabel": func(w entry.Warning) string {
		switch w {
		case entry.WarnFakeIP:
			return "это FakeIP-адрес sing-box: скорее всего, домен уже идёт через туннель — добавьте домен, а не IP"
		case entry.WarnPrivate:
			return "частный или служебный диапазон: через туннель его пускать незачем"
		}
		return string(w)
	},
	"ago":    ago,
	"static": staticURL,
	"stats":  func(l store.List, oob bool) statsData { return statsData{l, oob} },
	"add": func(n ...int) int {
		s := 0
		for _, v := range n {
			s += v
		}
		return s
	},
}

// ago — «5 мин назад», дальше недели — дата.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "только что"
	case d < time.Hour:
		return fmt.Sprintf("%d мин назад", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч назад", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d дн назад", int(d.Hours()/24))
	}
	return t.Local().Format("02.01.2006")
}

type pageTemplate struct {
	t *template.Template
}

// loadPages собирает каждую страницу отдельно: layout + все partials + pages/<имя>.html,
// чтобы блоки "content" разных страниц не перезаписывали друг друга.
// Отдельно собирается набор только из partials — для ответов htmx.
func loadPages() (map[string]*pageTemplate, *template.Template, error) {
	files, err := fs.Glob(assets, "templates/pages/*.html")
	if err != nil {
		return nil, nil, err
	}
	out := map[string]*pageTemplate{}
	for _, f := range files {
		name := strings.TrimSuffix(path.Base(f), ".html")
		t, err := template.New(name).Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/partials/*.html", f)
		if err != nil {
			return nil, nil, err
		}
		out[name] = &pageTemplate{t: t}
	}
	partials, err := template.New("partials").Funcs(funcs).ParseFS(assets, "templates/partials/*.html")
	if err != nil {
		return nil, nil, err
	}
	return out, partials, nil
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
	s.write(w, status, func(buf *bytes.Buffer) error { return pt.t.ExecuteTemplate(buf, "layout", p) })
}

// fragment — один или несколько partial-шаблонов подряд (основной ответ + out-of-band для htmx).
type fragment struct {
	name string
	data any
}

func (s *Server) renderFragments(w http.ResponseWriter, status int, frags ...fragment) {
	s.write(w, status, func(buf *bytes.Buffer) error {
		for _, f := range frags {
			if err := s.partials.ExecuteTemplate(buf, f.name, f.data); err != nil {
				return err
			}
		}
		return nil
	})
}

// write рендерит в буфер: ошибка шаблона не оставит полстраницы с кодом 200.
func (s *Server) write(w http.ResponseWriter, status int, fn func(*bytes.Buffer) error) {
	var buf bytes.Buffer
	if err := fn(&buf); err != nil {
		s.Log.Error("ошибка шаблона", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// assetVersion — хеш всей статики. Добавляется к ссылкам (?v=), поэтому статику можно кешировать
// надолго: после обновления сервиса меняется URL и браузер сразу берёт новые файлы.
var assetVersion = func() string {
	h := sha256.New()
	fs.WalkDir(assets, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}()

func staticURL(name string) string { return "/static/" + name + "?v=" + assetVersion }
