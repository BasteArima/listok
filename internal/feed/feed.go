// Package feed собирает содержимое фидов для роутеров (docs/feeds.md).
package feed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/BasteArima/listok/internal/entry"
	"github.com/BasteArima/listok/internal/store"
)

// Sentinel — первая строка каждого фида. Гарантирует, что rule-set на роутере не пуст и содержит
// доменный матчер: тогда forkop регистрирует его и в route-, и в DNS-правилах (D-013).
const Sentinel = "listok-sentinel.invalid"

// Content — собранный фид.
type Content struct {
	Body    []byte
	ETag    string // в кавычках, как в заголовке HTTP
	Entries int    // без сторожевой записи
}

// Builder собирает фиды и кеширует их по ключу «id:версия» входящих списков.
// Любая правка списка поднимает его версию, поэтому инвалидация нужна только при удалении
// списка (Forget): SQLite может отдать его id новому списку, и ключ совпадёт со старым.
type Builder struct {
	st    *store.Store
	mu    sync.Mutex
	cache map[int64]cached
}

type cached struct {
	key string
	c   Content
}

func NewBuilder(st *store.Store) *Builder {
	return &Builder{st: st, cache: map[int64]cached{}}
}

// Forget сбрасывает весь кеш: следующая отдача каждого фида соберёт его заново.
func (b *Builder) Forget() {
	b.mu.Lock()
	b.cache = map[int64]cached{}
	b.mu.Unlock()
}

func (b *Builder) Build(ctx context.Context, f store.Feed) (Content, error) {
	key, err := b.st.FeedSourceKey(ctx, f)
	if err != nil {
		return Content{}, err
	}
	b.mu.Lock()
	if c, ok := b.cache[f.ID]; ok && c.key == key {
		b.mu.Unlock()
		return c.c, nil
	}
	b.mu.Unlock()

	rows, err := b.st.FeedEntries(ctx, f)
	if err != nil {
		return Content{}, err
	}
	entries := make([]entry.Entry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, entry.Entry{Value: r.Value, Kind: entry.Kind(r.Kind)})
	}
	c := Render(entry.Compact(entries))

	b.mu.Lock()
	b.cache[f.ID] = cached{key: key, c: c}
	b.mu.Unlock()
	return c, nil
}

// Render — текст фида: сторожевая запись, затем записи по одной на строку, без комментариев
// (forkop при генерации конфига комментарии не вырезает, П-3).
func Render(entries []entry.Entry) Content {
	var sb strings.Builder
	sb.WriteString(Sentinel)
	sb.WriteByte('\n')
	n := 0
	for _, e := range entries {
		if e.Value == Sentinel {
			continue
		}
		sb.WriteString(e.Value)
		sb.WriteByte('\n')
		n++
	}
	body := []byte(sb.String())
	sum := sha256.Sum256(body)
	return Content{Body: body, ETag: `"` + hex.EncodeToString(sum[:])[:16] + `"`, Entries: n}
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewToken — 43 символа base62 (~256 бит). Только буквы и цифры: такую ссылку не испортит
// ни один валидатор URL в LuCI, и её удобно копировать.
func NewToken() string {
	const n = 43
	out := make([]byte, 0, n)
	buf := make([]byte, 64)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		for _, b := range buf {
			// 248 = 4·62: отбрасываем хвост, чтобы символы были равновероятны.
			if b < 248 && len(out) < n {
				out = append(out, base62[b%62])
			}
		}
	}
	return string(out)
}
