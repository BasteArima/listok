package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/BasteArima/listok/internal/routers"
	"github.com/BasteArima/listok/internal/store"
)

// Секции forkop на странице роутера (docs/ui.md, «Роутеры»): по карточке на секцию, в ней
// списки-переключатели. Подключённая секция — это фид, но слово «фид» в интерфейсе не звучит.

// agentOnline — агент выходил на связь не дольше этого назад (long-poll держит связь постоянно).
const agentOnline = 3 * time.Minute

// listChip — список-переключатель в карточке секции.
type listChip struct {
	ID    int64
	Title string
	Count int
	On    bool
}

// sectionView — карточка секции.
type sectionView struct {
	RouterID int64
	Name     string
	Action   string    // тип секции в forkop (connection, dns, …); пусто — неизвестен
	Feed     *feedView // nil — секция не подключена к listok
	// Missing — секция подключена, но агент её в forkop не видит.
	Missing bool
	// Valid — название подходит для listok (латиница, цифры, _): иначе подключить нельзя.
	Valid    bool
	HasAgent bool
	// State подключённой секции: attaching — агент ещё не подключил её к forkop; applying —
	// изменение ещё не применено; applied; error; off — выключена; без агента — never, fresh, stale.
	State string
	Lists []listChip
}

// Pending — карточка сама переспрашивает сервер, пока изменение доходит до роутера.
func (v sectionView) Pending() bool {
	return v.State == "attaching" || v.State == "applying"
}

func sectionState(rt store.Router, f routers.FeedState) string {
	switch {
	case !f.Enabled:
		return "off"
	case !rt.HasAgent:
		return f.Status
	case f.AppliedAt == nil:
		return "attaching"
	case !f.AppliedOK:
		return "error"
	case f.AppliedETag == f.Current.ETag:
		return "applied"
	}
	return "applying"
}

func (s *Server) newSectionView(rt store.Router, name, action string, f *routers.FeedState, lists []store.List) sectionView {
	v := sectionView{RouterID: rt.ID, Name: name, Action: action, Valid: routers.ValidSection(name), HasAgent: rt.HasAgent}
	if f == nil {
		return v
	}
	set := map[int64]bool{}
	for _, id := range f.ListIDs {
		set[id] = true
	}
	v.Feed = &feedView{FeedState: *f, URL: s.feedURL(f.Token), ListSet: set}
	v.State = sectionState(rt, *f)
	for _, l := range lists {
		v.Lists = append(v.Lists, listChip{ID: l.ID, Title: l.Title, Count: l.EnabledCount, On: set[l.ID]})
	}
	return v
}

// sections — карточки в порядке секций forkop; подключённые секции, которых агент в forkop
// не видит (или агент секций не присылал), идут следом.
func (s *Server) sections(rt store.Router, feeds []routers.FeedState, lists []store.List) []sectionView {
	bySection := make(map[string]*routers.FeedState, len(feeds))
	for i := range feeds {
		bySection[feeds[i].Section] = &feeds[i]
	}
	var out []sectionView
	seen := map[string]bool{}
	for _, fs := range rt.ForkopSections {
		// Подключённая по названию или по UCI-имени: агент находит секцию forkop обоими способами.
		f := bySection[fs.Name]
		if f == nil && fs.UCI != "" {
			f = bySection[fs.UCI]
		}
		out = append(out, s.newSectionView(rt, fs.Name, fs.Action, f, lists))
		if f != nil {
			seen[f.Section] = true
		}
	}
	for i := range feeds {
		if seen[feeds[i].Section] {
			continue
		}
		v := s.newSectionView(rt, feeds[i].Section, "", &feeds[i], lists)
		v.Missing = rt.ForkopSections != nil
		out = append(out, v)
	}
	return out
}

// ownerLists — списки, видимые владельцу роутера: из них собираются секции.
func (s *Server) ownerLists(ctx context.Context, rt store.Router) ([]store.List, error) {
	owner, err := s.Store.UserByID(ctx, rt.OwnerID)
	if err != nil {
		return nil, err
	}
	return s.Lists.Visible(ctx, owner)
}

// sectionCard отдаёт карточку одной подключённой секции (htmx).
func (s *Server) sectionCard(w http.ResponseWriter, r *http.Request, rt store.Router, feedID int64) {
	ctx := r.Context()
	feeds, err := s.Routers.Feeds(ctx, rt, 0)
	if err != nil {
		s.serverError(w, err)
		return
	}
	lists, err := s.ownerLists(ctx, rt)
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, v := range s.sections(rt, feeds, lists) {
		if v.Feed != nil && v.Feed.ID == feedID {
			s.renderFragments(w, http.StatusOK, fragment{"section-card", v})
			return
		}
	}
	http.NotFound(w, r)
}

// sectionCardGet — GET /routers/{id}/sections/{feed}: карточка секции (её переспрашивает сама
// карточка, пока изменение доходит до роутера).
func (s *Server) sectionCardGet(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routerFromPath(w, r)
	if !ok {
		return
	}
	feedID, err := strconv.ParseInt(r.PathValue("feed"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.sectionCard(w, r, rt, feedID)
}

// feedLists — POST /feeds/{id}/lists: состав секции. Карточка шлёт его при каждом переключении
// списка (без кнопки «сохранить») и получает себя же обратно; обычная форма — редирект.
func (s *Server) feedLists(w http.ResponseWriter, r *http.Request) {
	feedID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u := userFrom(r.Context())
	routerID, err := s.Routers.SetFeedLists(r.Context(), *u, feedID, listIDsFrom(r))
	if errors.Is(err, routers.ErrBadList) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.routerError(w, r, err)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		s.redirect(w, r, "/routers/"+strconv.FormatInt(routerID, 10))
		return
	}
	rt, err := s.Routers.Get(r.Context(), *u, routerID)
	if err != nil {
		s.routerError(w, r, err)
		return
	}
	s.sectionCard(w, r, rt, feedID)
}
