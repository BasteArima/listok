package lists

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BasteArima/listok/internal/entry"
	"github.com/BasteArima/listok/internal/store"
)

// Файл списка — формат обмена списками (docs/normalization.md, «Файл списка»):
// запись на строку, комментарий после « # », строки с # в начале пропускаются.

const (
	// MaxImportBytes — предельный размер файла импорта.
	MaxImportBytes = 4 << 20
	// MaxImportEntries — сколько записей можно импортировать за раз.
	MaxImportEntries = 50000
)

var (
	ErrBadFile = errors.New("файл должен быть текстом в UTF-8")
	ErrBigFile = fmt.Errorf("в файле больше %d записей", MaxImportEntries)
)

// Export — включённые записи списка файлом: домены, затем IPv4 и IPv6. Возвращает и число записей.
func (s *Service) Export(ctx context.Context, l store.List) ([]byte, int, error) {
	all, err := s.st.AllEntries(ctx, l.ID)
	if err != nil {
		return nil, 0, err
	}
	var es []store.Entry
	for _, e := range all {
		if e.Enabled {
			es = append(es, e)
		}
	}
	sortForFile(es)
	return formatFile(l, es, s.now()), len(es), nil
}

func formatFile(l store.List, es []store.Entry, now time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# listok: %s\n", oneLine(l.Title))
	if d := oneLine(l.Description); d != "" {
		fmt.Fprintf(&b, "# %s\n", d)
	}
	fmt.Fprintf(&b, "# адрес %s, версия %d, выгружено %s, записей: %d\n", l.Slug, l.Version, now.Format("2006-01-02 15:04"), len(es))
	for _, e := range es {
		b.WriteString(e.Value)
		if c := oneLine(e.Comment); c != "" {
			b.WriteString(" # " + c)
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

var kindOrder = map[string]int{"domain": 0, "cidr4": 1, "cidr6": 2}

func sortForFile(es []store.Entry) {
	slices.SortStableFunc(es, func(a, b store.Entry) int {
		if c := cmp.Compare(kindOrder[a.Kind], kindOrder[b.Kind]); c != 0 {
			return c
		}
		pa, errA := netip.ParsePrefix(a.Value)
		pb, errB := netip.ParsePrefix(b.Value)
		if errA == nil && errB == nil {
			if c := pa.Addr().Compare(pb.Addr()); c != 0 {
				return c
			}
			return cmp.Compare(pa.Bits(), pb.Bits())
		}
		return strings.Compare(a.Value, b.Value)
	})
}

// Import добавляет записи из файла одной версией (source=import), у каждой свой комментарий.
// Домены берутся как есть (ExactHost): в выгрузке они уже нормализованы, и поддомен,
// добавленный флажком «только этот хост», не должен сократиться до родителя.
// Ссылки сокращаются как при обычном добавлении: в выгрузке их не бывает.
func (s *Service) Import(ctx context.Context, u store.User, l store.List, data []byte, name string) ([]Item, error) {
	if !CanEdit(l) {
		return nil, ErrForbidden
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, ErrBadFile
	}
	items := parseFile(string(data))
	if len(items) > MaxImportEntries {
		return nil, ErrBigFile
	}
	if err := s.annotate(ctx, u, items, l.ID, false); err != nil {
		return nil, err
	}
	if err := s.addItems(ctx, u, l, items, "import", importMessage(name)); err != nil {
		return nil, err
	}
	return items, nil
}

func parseFile(text string) []Item {
	seen := map[string]bool{}
	var out []Item
	for line := range strings.Lines(strings.TrimPrefix(text, "\uFEFF")) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		value, comment := splitComment(line)
		for _, tok := range entry.Tokens(value) {
			r := entry.Parse(tok, entry.Options{ExactHost: !strings.ContainsAny(tok, "/:?#@")})
			if r.Err == nil {
				if seen[r.Entry.Value] {
					r.Err = entry.ErrDuplicate
				}
				seen[r.Entry.Value] = true
			}
			out = append(out, Item{Result: r, Comment: comment})
		}
	}
	return out
}

// splitComment отделяет комментарий: всё после первого #, перед которым пробел или таб.
// # без пробела перед ним — часть значения (якорь в ссылке).
func splitComment(line string) (value, comment string) {
	for i := 1; i < len(line); i++ {
		if line[i] == '#' && (line[i-1] == ' ' || line[i-1] == '\t') {
			return strings.TrimSpace(line[:i]), cutRunes(strings.TrimSpace(line[i+1:]), maxComment)
		}
	}
	return line, ""
}

// importMessage — подпись версии в истории: имя файла без пути, не длиннее 60 символов.
func importMessage(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name = oneLine(name); name == "" {
		return "импорт из файла"
	}
	if utf8.RuneCountInString(name) > 60 {
		name = cutRunes(name, 59) + "…"
	}
	return "импорт из файла " + name
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
