// Package lists — сервис списков и записей: права, предпросмотр ввода, добавление и правка.
// Индекс покрытия (entry.Index) держится в памяти и обновляется после каждой успешной правки.
package lists

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BasteArima/listok/internal/entry"
	"github.com/BasteArima/listok/internal/history"
	"github.com/BasteArima/listok/internal/store"
)

var (
	ErrNotFound    = errors.New("список не найден")
	ErrForbidden   = errors.New("нет прав на изменение этого списка")
	ErrBadSlug     = errors.New("адрес: латиница в нижнем регистре, цифры и дефис, до 40 символов")
	ErrBadTitle    = errors.New("название: от 1 до 80 символов")
	ErrLongComment = errors.New("комментарий: до 200 символов")
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

type Service struct {
	st  *store.Store
	idx *entry.Index
	log *slog.Logger
	now func() time.Time
}

func New(st *store.Store, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, idx: entry.NewIndex(), log: log, now: now}
}

// LoadIndex заполняет индекс покрытия включёнными записями из БД. Вызывается при старте.
func (s *Service) LoadIndex(ctx context.Context) (int, error) {
	all, err := s.st.EnabledEntries(ctx)
	if err != nil {
		return 0, err
	}
	for _, e := range all {
		s.idx.Add(e.ListID, entry.Entry{Value: e.Value, Kind: entry.Kind(e.Kind)})
	}
	return len(all), nil
}

func CanEdit(l store.List) bool {
	return l.Role == "owner" || l.Role == "editor" || l.Role == "admin"
}

func (s *Service) Visible(ctx context.Context, u store.User) ([]store.List, error) {
	return s.st.VisibleLists(ctx, u.ID, u.IsAdmin)
}

// Get — список, если пользователь его видит. Невидимый и несуществующий неотличимы: ErrNotFound.
func (s *Service) Get(ctx context.Context, u store.User, slug string) (store.List, error) {
	l, err := s.st.ListBySlug(ctx, slug, u.ID, u.IsAdmin)
	if errors.Is(err, store.ErrNotFound) || (err == nil && l.Role == "") {
		return store.List{}, ErrNotFound
	}
	return l, err
}

func (s *Service) Create(ctx context.Context, u store.User, slug, title, description string) (store.List, error) {
	slug = strings.TrimSpace(slug)
	title = strings.TrimSpace(title)
	if !slugRe.MatchString(slug) {
		return store.List{}, ErrBadSlug
	}
	if n := utf8.RuneCountInString(title); n < 1 || n > 80 {
		return store.List{}, ErrBadTitle
	}
	if _, err := s.st.CreateList(ctx, slug, title, strings.TrimSpace(description), u.ID, s.now()); err != nil {
		return store.List{}, err
	}
	return s.Get(ctx, u, slug)
}

func (s *Service) Entries(ctx context.Context, l store.List, f store.EntryFilter) ([]store.Entry, error) {
	return s.st.Entries(ctx, l.ID, f)
}

func (s *Service) Recent(ctx context.Context, u store.User, limit int) ([]store.RecentChange, error) {
	return s.st.RecentChanges(ctx, u.ID, u.IsAdmin, limit)
}

// Cover — запись из видимого списка, которая покрывает новую (или станет лишней).
type Cover struct {
	ListID    int64
	ListSlug  string
	ListTitle string
	Value     string
}

// Item — разбор одного токена ввода с покрытием и итогом добавления.
type Item struct {
	entry.Result
	CoveredBy []Cover
	Covers    []Cover
	// Status после Add: added, exists (уже есть ровно это), covered (покрыто записью этого же списка), error.
	Status string
	// CoveredInTarget — покрыто записью целевого списка: добавлять бессмысленно.
	CoveredInTarget bool
}

func (it Item) OK() bool { return it.Err == nil }

// Preview разбирает ввод и показывает, что получится, не меняя данных.
// targetID — список, куда собираются добавлять (0 — не выбран).
func (s *Service) Preview(ctx context.Context, u store.User, input string, opt entry.Options, targetID int64) ([]Item, error) {
	visible, err := s.Visible(ctx, u)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]store.List, len(visible))
	for _, l := range visible {
		byID[l.ID] = l
	}
	toCovers := func(refs []entry.Ref) []Cover {
		var out []Cover
		for _, r := range refs {
			if l, ok := byID[r.ListID]; ok {
				out = append(out, Cover{ListID: l.ID, ListSlug: l.Slug, ListTitle: l.Title, Value: r.Entry.Value})
			}
		}
		return out
	}

	var items []Item
	for _, r := range entry.ParseMany(input, opt) {
		it := Item{Result: r}
		if r.Err == nil {
			it.CoveredBy = toCovers(s.idx.CoveredBy(r.Entry))
			it.Covers = toCovers(s.idx.Covers(r.Entry))
			for _, c := range it.CoveredBy {
				if c.ListID == targetID {
					it.CoveredInTarget = true
				}
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// Add разбирает ввод и добавляет подходящие записи в список одной версией.
// Ошибочные токены и уже покрытые записью этого же списка пропускаются с пометкой в Status.
func (s *Service) Add(ctx context.Context, u store.User, l store.List, input string, opt entry.Options, comment, source string) ([]Item, error) {
	if !CanEdit(l) {
		return nil, ErrForbidden
	}
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > 200 {
		return nil, ErrLongComment
	}
	items, err := s.Preview(ctx, u, input, opt, l.ID)
	if err != nil {
		return nil, err
	}

	var added []entry.Entry
	_, _, err = s.st.Mutate(ctx, store.MutateOptions{ListID: l.ID, UserID: u.ID, Source: source, Now: s.now()},
		func(m *store.Mutation) error {
			added = added[:0]
			for i := range items {
				it := &items[i]
				switch {
				case !it.OK():
					it.Status = "error"
				case it.CoveredInTarget:
					it.Status = "covered"
					for _, c := range it.CoveredBy {
						if c.ListID == l.ID && c.Value == it.Entry.Value {
							it.Status = "exists"
						}
					}
				default:
					ok, err := m.Add(it.Entry.Value, string(it.Entry.Kind), comment)
					if err != nil {
						return err
					}
					if ok {
						it.Status = "added"
						added = append(added, it.Entry)
					} else {
						// Запись есть, но выключена: в индексе её нет, поэтому CoveredInTarget не сработал.
						it.Status = "exists"
					}
				}
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	for _, e := range added {
		s.idx.Add(l.ID, e)
	}
	return items, nil
}

func (s *Service) SetEnabled(ctx context.Context, u store.User, l store.List, id int64, enabled bool) (store.Entry, error) {
	return s.update(ctx, u, l, id, nil, &enabled)
}

func (s *Service) SetComment(ctx context.Context, u store.User, l store.List, id int64, comment string) (store.Entry, error) {
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > 200 {
		return store.Entry{}, ErrLongComment
	}
	return s.update(ctx, u, l, id, &comment, nil)
}

func (s *Service) update(ctx context.Context, u store.User, l store.List, id int64, comment *string, enabled *bool) (store.Entry, error) {
	if !CanEdit(l) {
		return store.Entry{}, ErrForbidden
	}
	var before, after store.Entry
	_, _, err := s.st.Mutate(ctx, store.MutateOptions{ListID: l.ID, UserID: u.ID, Source: "web", Now: s.now()},
		func(m *store.Mutation) error {
			var err error
			before, after, err = m.Update(id, comment, enabled)
			return err
		})
	if errors.Is(err, store.ErrNotFound) {
		return store.Entry{}, ErrNotFound
	}
	if err != nil {
		return store.Entry{}, err
	}
	e := entry.Entry{Value: after.Value, Kind: entry.Kind(after.Kind)}
	switch {
	case !before.Enabled && after.Enabled:
		s.idx.Add(l.ID, e)
	case before.Enabled && !after.Enabled:
		s.idx.Remove(l.ID, e)
	}
	return after, nil
}

func (s *Service) Delete(ctx context.Context, u store.User, l store.List, id int64) error {
	if !CanEdit(l) {
		return ErrForbidden
	}
	var removed store.Entry
	_, _, err := s.st.Mutate(ctx, store.MutateOptions{ListID: l.ID, UserID: u.ID, Source: "web", Now: s.now()},
		func(m *store.Mutation) error {
			var err error
			removed, err = m.Remove(id)
			return err
		})
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if removed.Enabled {
		s.idx.Remove(l.ID, entry.Entry{Value: removed.Value, Kind: entry.Kind(removed.Kind)})
	}
	return nil
}

var ErrBadVersion = errors.New("такой версии нет")

func (s *Service) Versions(ctx context.Context, l store.List, before int64, limit int) ([]store.Version, error) {
	return s.st.Versions(ctx, l.ID, before, limit)
}

func (s *Service) Version(ctx context.Context, l store.List, version int64) (store.Version, []store.Change, error) {
	v, cs, err := s.st.VersionWithChanges(ctx, l.ID, version)
	if errors.Is(err, store.ErrNotFound) {
		return store.Version{}, nil, ErrBadVersion
	}
	return v, cs, err
}

// Rollback возвращает список к состоянию на версию target одной новой версией (source='rollback').
// target = 0 — состояние до первого изменения (пустой список). Возвращает номер новой версии,
// 0 — если состояние уже совпадает.
func (s *Service) Rollback(ctx context.Context, u store.User, l store.List, target int64) (int64, error) {
	if !CanEdit(l) {
		return 0, ErrForbidden
	}
	newVersion, changes, err := s.st.Mutate(ctx, store.MutateOptions{
		ListID: l.ID, UserID: u.ID, Source: "rollback", Message: fmt.Sprintf("откат к версии %d", target), Now: s.now(),
	}, func(m *store.Mutation) error {
		cur, err := m.CurrentVersion()
		if err != nil {
			return err
		}
		if target < 0 || target > cur {
			return ErrBadVersion
		}
		entries, err := m.Entries()
		if err != nil {
			return err
		}
		current := make(map[string]history.State, len(entries))
		ids := make(map[string]int64, len(entries))
		for _, e := range entries {
			current[e.Value] = history.State{Kind: e.Kind, Comment: e.Comment, Enabled: e.Enabled}
			ids[e.Value] = e.ID
		}
		newer, err := m.ChangesAfter(target)
		if err != nil {
			return err
		}
		plan := history.Diff(current, history.Reconstruct(current, newer))
		for _, v := range plan.Remove {
			if _, err := m.Remove(ids[v]); err != nil {
				return err
			}
		}
		for _, up := range plan.Update {
			if _, _, err := m.Update(ids[up.Value], up.Comment, up.Enabled); err != nil {
				return err
			}
		}
		for _, a := range plan.Add {
			if _, err := m.AddWith(a.Value, a.State.Kind, a.State.Comment, a.State.Enabled); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.applyToIndex(l.ID, changes)
	return newVersion, nil
}

// applyToIndex переносит в индекс покрытия изменения включённости из записанных изменений.
func (s *Service) applyToIndex(listID int64, changes []store.Change) {
	for _, c := range changes {
		e := entry.Entry{Value: c.Value, Kind: entry.Kind(c.Kind)}
		switch c.Op {
		case "add":
			if c.NewEnabled == nil || *c.NewEnabled {
				s.idx.Add(listID, e)
			}
		case "remove":
			if c.OldEnabled == nil || *c.OldEnabled {
				s.idx.Remove(listID, e)
			}
		case "update":
			if c.NewEnabled != nil {
				if *c.NewEnabled {
					s.idx.Add(listID, e)
				} else {
					s.idx.Remove(listID, e)
				}
			}
		}
	}
}
