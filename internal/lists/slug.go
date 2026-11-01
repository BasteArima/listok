package lists

import (
	"strconv"
	"strings"
)

// translit — русские буквы латиницей для адреса списка (как в загранпаспорте, без апострофов).
var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "i", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "iu", 'я': "ia",
}

// slugify — адрес списка из названия: «Общий список» → obshchii-spisok. Пусто — "list".
func slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		var part string
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			part = string(r)
		default:
			t, ok := translit[r]
			if !ok {
				dash = b.Len() > 0
				continue
			}
			part = t
		}
		if part == "" {
			continue
		}
		if dash {
			b.WriteByte('-')
			dash = false
		}
		b.WriteString(part)
	}
	s := b.String()
	if len(s) > maxSlug {
		s = strings.TrimRight(s[:maxSlug], "-")
	}
	if s == "" {
		return "list"
	}
	return s
}

// withSuffix — slug-2, slug-3…: укорачивает основу, чтобы уложиться в maxSlug.
func withSuffix(slug string, n int) string {
	suffix := "-" + strconv.Itoa(n)
	if len(slug)+len(suffix) > maxSlug {
		slug = strings.TrimRight(slug[:maxSlug-len(suffix)], "-")
	}
	return slug + suffix
}
