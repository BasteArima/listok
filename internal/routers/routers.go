// Package routers — роутеры пользователя и их фиды: права, состав, токены, статус опроса.
package routers

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BasteArima/listok/internal/feed"
	"github.com/BasteArima/listok/internal/store"
)

var (
	ErrNotFound   = errors.New("роутер не найден")
	ErrBadName    = errors.New("название роутера: от 1 до 60 символов")
	ErrBadSection = errors.New("секция forkop: латиница, цифры и подчёркивание, до 32 символов (например, main)")
	ErrBadList    = errors.New("в фид можно включить только видимые вам списки")
	ErrLongNotes  = errors.New("заметки: до 500 символов")
)

var sectionRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

type Service struct {
	st      *store.Store
	builder *feed.Builder
	now     func() time.Time
}

func New(st *store.Store, builder *feed.Builder, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, builder: builder, now: now}
}

func (s *Service) List(ctx context.Context, u store.User) ([]store.Router, error) {
	return s.st.Routers(ctx, u.ID, u.IsAdmin)
}

// Get — роутер, если он принадлежит пользователю (или пользователь админ). Иначе ErrNotFound.
func (s *Service) Get(ctx context.Context, u store.User, id int64) (store.Router, error) {
	r, err := s.st.RouterByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && r.OwnerID != u.ID && !u.IsAdmin) {
		return store.Router{}, ErrNotFound
	}
	return r, err
}

func validate(name, notes string) (string, string, error) {
	name, notes = strings.TrimSpace(name), strings.TrimSpace(notes)
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 {
		return "", "", ErrBadName
	}
	if utf8.RuneCountInString(notes) > 500 {
		return "", "", ErrLongNotes
	}
	return name, notes, nil
}

func (s *Service) Create(ctx context.Context, u store.User, name, notes string) (int64, error) {
	name, notes, err := validate(name, notes)
	if err != nil {
		return 0, err
	}
	return s.st.CreateRouter(ctx, name, notes, u.ID, s.now())
}

func (s *Service) Update(ctx context.Context, u store.User, id int64, name, notes string) error {
	if _, err := s.Get(ctx, u, id); err != nil {
		return err
	}
	name, notes, err := validate(name, notes)
	if err != nil {
		return err
	}
	return s.st.UpdateRouter(ctx, id, name, notes)
}

func (s *Service) Delete(ctx context.Context, u store.User, id int64) error {
	if _, err := s.Get(ctx, u, id); err != nil {
		return err
	}
	return s.st.DeleteRouter(ctx, id)
}

// FeedState — фид с текущим содержимым и статусом опроса.
type FeedState struct {
	store.Feed
	Current feed.Content
	// Status: never — роутер ещё не забирал; fresh — забрал актуальную версию; stale — забрал устаревшую.
	Status  string
	Fetches []store.FeedFetch
}

func (s *Service) Feeds(ctx context.Context, r store.Router, withFetches int) ([]FeedState, error) {
	feeds, err := s.st.FeedsByRouter(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	out := make([]FeedState, 0, len(feeds))
	for _, f := range feeds {
		c, err := s.builder.Build(ctx, f)
		if err != nil {
			return nil, err
		}
		fs := FeedState{Feed: f, Current: c}
		switch {
		case f.LastFetchAt == nil:
			fs.Status = "never"
		case f.LastETag == c.ETag:
			fs.Status = "fresh"
		default:
			fs.Status = "stale"
		}
		if withFetches > 0 {
			if fs.Fetches, err = s.st.FeedFetches(ctx, f.ID, withFetches); err != nil {
				return nil, err
			}
		}
		out = append(out, fs)
	}
	return out, nil
}

// checkLists — все списки видимы пользователю-владельцу роутера.
func (s *Service) checkLists(ctx context.Context, u store.User, listIDs []int64) error {
	visible, err := s.st.VisibleLists(ctx, u.ID, u.IsAdmin)
	if err != nil {
		return err
	}
	ok := make(map[int64]bool, len(visible))
	for _, l := range visible {
		ok[l.ID] = true
	}
	for _, id := range listIDs {
		if !ok[id] {
			return ErrBadList
		}
	}
	return nil
}

// owner — владелец роутера: состав фида проверяется по его правам, даже если правит админ.
func (s *Service) owner(ctx context.Context, r store.Router) (store.User, error) {
	return s.st.UserByID(ctx, r.OwnerID)
}

func (s *Service) CreateFeed(ctx context.Context, u store.User, routerID int64, section string, listIDs []int64) (int64, error) {
	r, err := s.Get(ctx, u, routerID)
	if err != nil {
		return 0, err
	}
	section = strings.TrimSpace(section)
	if !sectionRe.MatchString(section) {
		return 0, ErrBadSection
	}
	owner, err := s.owner(ctx, r)
	if err != nil {
		return 0, err
	}
	if err := s.checkLists(ctx, owner, listIDs); err != nil {
		return 0, err
	}
	return s.st.CreateFeed(ctx, r.ID, section, feed.NewToken(), listIDs, s.now())
}

// feedOf — фид и его роутер с проверкой, что роутер принадлежит пользователю.
func (s *Service) feedOf(ctx context.Context, u store.User, feedID int64) (store.Feed, store.Router, error) {
	f, err := s.st.FeedByID(ctx, feedID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Feed{}, store.Router{}, ErrNotFound
	}
	if err != nil {
		return store.Feed{}, store.Router{}, err
	}
	r, err := s.Get(ctx, u, f.RouterID)
	return f, r, err
}

func (s *Service) SetFeedLists(ctx context.Context, u store.User, feedID int64, listIDs []int64) (int64, error) {
	f, r, err := s.feedOf(ctx, u, feedID)
	if err != nil {
		return 0, err
	}
	owner, err := s.owner(ctx, r)
	if err != nil {
		return 0, err
	}
	if err := s.checkLists(ctx, owner, listIDs); err != nil {
		return 0, err
	}
	return r.ID, s.st.SetFeedLists(ctx, f.ID, listIDs)
}

func (s *Service) RegenerateToken(ctx context.Context, u store.User, feedID int64) (int64, error) {
	f, r, err := s.feedOf(ctx, u, feedID)
	if err != nil {
		return 0, err
	}
	return r.ID, s.st.SetFeedToken(ctx, f.ID, feed.NewToken())
}

func (s *Service) SetFeedEnabled(ctx context.Context, u store.User, feedID int64, enabled bool) (int64, error) {
	f, r, err := s.feedOf(ctx, u, feedID)
	if err != nil {
		return 0, err
	}
	return r.ID, s.st.SetFeedEnabled(ctx, f.ID, enabled)
}

func (s *Service) DeleteFeed(ctx context.Context, u store.User, feedID int64) (int64, error) {
	f, r, err := s.feedOf(ctx, u, feedID)
	if err != nil {
		return 0, err
	}
	return r.ID, s.st.DeleteFeed(ctx, f.ID)
}

// Serve — содержимое фида по токену для роутера. Выключенный или неизвестный фид — ErrNotFound.
// Опрос пишется в журнал; ip — адрес клиента, notModified — совпал If-None-Match.
func (s *Service) Serve(ctx context.Context, token, ip, ifNoneMatch string) (c feed.Content, notModified bool, err error) {
	if token == "" {
		return feed.Content{}, false, ErrNotFound
	}
	f, err := s.st.FeedByToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !f.Enabled) {
		return feed.Content{}, false, ErrNotFound
	}
	if err != nil {
		return feed.Content{}, false, err
	}
	c, err = s.builder.Build(ctx, f)
	if err != nil {
		return feed.Content{}, false, err
	}
	notModified = etagMatch(ifNoneMatch, c.ETag)
	status := 200
	if notModified {
		status = 304
	}
	if err := s.st.RecordFetch(ctx, f.ID, s.now(), ip, status, c.ETag); err != nil {
		return feed.Content{}, false, err
	}
	return c, notModified, nil
}

// etagMatch — If-None-Match содержит наш ETag (допускаем список и слабую форму W/).
func etagMatch(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == etag || part == "*" {
			return true
		}
	}
	return false
}
