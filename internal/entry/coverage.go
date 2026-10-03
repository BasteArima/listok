package entry

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
	"sync"
)

// Ref — запись в конкретном списке.
type Ref struct {
	ListID int64
	Entry  Entry
}

// Index — индекс записей всех списков в памяти для проверки покрытия.
// forkop ставит домены как domain_suffix, поэтому "youtube.com" покрывает "m.youtube.com".
type Index struct {
	mu       sync.RWMutex
	domains  map[string]map[int64]struct{}
	prefixes map[netip.Prefix]map[int64]struct{}
}

func NewIndex() *Index {
	return &Index{
		domains:  map[string]map[int64]struct{}{},
		prefixes: map[netip.Prefix]map[int64]struct{}{},
	}
}

func (ix *Index) Add(listID int64, e Entry) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if e.IsDomain() {
		addTo(ix.domains, e.Value, listID)
	} else if p, ok := e.Prefix(); ok {
		addTo(ix.prefixes, p, listID)
	}
}

func (ix *Index) Remove(listID int64, e Entry) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if e.IsDomain() {
		removeFrom(ix.domains, e.Value, listID)
	} else if p, ok := e.Prefix(); ok {
		removeFrom(ix.prefixes, p, listID)
	}
}

// CoveredBy — записи, которые покрывают e, включая точное совпадение.
func (ix *Index) CoveredBy(e Entry) []Ref {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []Ref
	if e.IsDomain() {
		for s := e.Value; ; {
			for id := range ix.domains[s] {
				out = append(out, Ref{id, Entry{s, KindDomain}})
			}
			_, rest, ok := strings.Cut(s, ".")
			if !ok {
				break
			}
			s = rest
		}
	} else if q, ok := e.Prefix(); ok {
		for p, ids := range ix.prefixes {
			if p.Addr().BitLen() == q.Addr().BitLen() && p.Bits() <= q.Bits() && p.Contains(q.Addr()) {
				for id := range ids {
					out = append(out, Ref{id, prefixEntry(p)})
				}
			}
		}
	}
	sortRefs(out)
	return out
}

// Covers — записи строго уже e, которые станут лишними после её добавления.
func (ix *Index) Covers(e Entry) []Ref {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []Ref
	if e.IsDomain() {
		suffix := "." + e.Value
		for d, ids := range ix.domains {
			if strings.HasSuffix(d, suffix) {
				for id := range ids {
					out = append(out, Ref{id, Entry{d, KindDomain}})
				}
			}
		}
	} else if q, ok := e.Prefix(); ok {
		for p, ids := range ix.prefixes {
			if p.Addr().BitLen() == q.Addr().BitLen() && p.Bits() > q.Bits() && q.Contains(p.Addr()) {
				for id := range ids {
					out = append(out, Ref{id, prefixEntry(p)})
				}
			}
		}
	}
	sortRefs(out)
	return out
}

// Compact убирает повторы и записи, покрытые другими, и сортирует:
// сначала домены по алфавиту, потом IPv4 и IPv6 по адресу. Используется при сборке фида.
func Compact(entries []Entry) []Entry {
	domainSet := map[string]bool{}
	var prefixes []netip.Prefix
	for _, e := range entries {
		if e.IsDomain() {
			domainSet[e.Value] = true
		} else if p, ok := e.Prefix(); ok {
			prefixes = append(prefixes, p.Masked())
		}
	}

	var domains []string
	for d := range domainSet {
		covered := false
		for s := d; ; {
			_, rest, ok := strings.Cut(s, ".")
			if !ok {
				break
			}
			if domainSet[rest] {
				covered = true
				break
			}
			s = rest
		}
		if !covered {
			domains = append(domains, d)
		}
	}
	slices.Sort(domains)

	// После сортировки по адресу и длине маски покрывающая подсеть всегда идёт раньше
	// покрытых, поэтому достаточно сравнивать с последней оставленной того же семейства.
	slices.SortFunc(prefixes, comparePrefix)
	out := make([]Entry, 0, len(domains)+len(prefixes))
	for _, d := range domains {
		out = append(out, Entry{d, KindDomain})
	}
	var last4, last6 netip.Prefix
	for _, p := range prefixes {
		last := &last6
		if p.Addr().Is4() {
			last = &last4
		}
		if last.IsValid() && last.Contains(p.Addr()) {
			continue
		}
		*last = p
		out = append(out, prefixEntry(p))
	}
	return out
}

func comparePrefix(a, b netip.Prefix) int {
	if c := cmp.Compare(a.Addr().BitLen(), b.Addr().BitLen()); c != 0 {
		return c
	}
	if c := a.Addr().Compare(b.Addr()); c != 0 {
		return c
	}
	return cmp.Compare(a.Bits(), b.Bits())
}

func prefixEntry(p netip.Prefix) Entry {
	if p.Addr().Is4() {
		return Entry{p.String(), KindCIDR4}
	}
	return Entry{p.String(), KindCIDR6}
}

func sortRefs(refs []Ref) {
	slices.SortFunc(refs, func(a, b Ref) int {
		if c := cmp.Compare(a.ListID, b.ListID); c != 0 {
			return c
		}
		return strings.Compare(a.Entry.Value, b.Entry.Value)
	})
}

func addTo[K comparable](m map[K]map[int64]struct{}, k K, id int64) {
	if m[k] == nil {
		m[k] = map[int64]struct{}{}
	}
	m[k][id] = struct{}{}
}

func removeFrom[K comparable](m map[K]map[int64]struct{}, k K, id int64) {
	delete(m[k], id)
	if len(m[k]) == 0 {
		delete(m, k)
	}
}
