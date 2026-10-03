// Package history — восстановление состояния списка на прошлую версию и план отката.
// Чистые функции без БД (docs/decisions.md, D-017).
//
// Откат к версии N не переписывает историю: берём текущее состояние, в обратном порядке
// отменяем изменения всех версий новее N, получаем целевое состояние и применяем разницу
// одной новой версией с source='rollback'.
package history

import (
	"maps"
	"slices"

	"github.com/BasteArima/listok/internal/store"
)

// State — состояние одной записи.
type State struct {
	Kind    string
	Comment string
	Enabled bool
}

// Reconstruct возвращает состояние списка до изменений newerFirst.
// newerFirst — версии от новых к старым, внутри версии изменения в порядке применения.
func Reconstruct(current map[string]State, newerFirst [][]store.Change) map[string]State {
	m := maps.Clone(current)
	if m == nil {
		m = map[string]State{}
	}
	for _, version := range newerFirst {
		for i := len(version) - 1; i >= 0; i-- {
			c := version[i]
			switch c.Op {
			case "add":
				delete(m, c.Value)
			case "remove":
				st := State{Kind: c.Kind, Enabled: true}
				if c.OldComment != nil {
					st.Comment = *c.OldComment
				}
				if c.OldEnabled != nil {
					st.Enabled = *c.OldEnabled
				}
				m[c.Value] = st
			case "update":
				st, ok := m[c.Value]
				if !ok {
					continue
				}
				if c.OldComment != nil {
					st.Comment = *c.OldComment
				}
				if c.OldEnabled != nil {
					st.Enabled = *c.OldEnabled
				}
				m[c.Value] = st
			}
		}
	}
	return m
}

type Update struct {
	Value   string
	Comment *string
	Enabled *bool
}

type Add struct {
	Value string
	State State
}

// Plan — что сделать с текущим состоянием, чтобы получить целевое.
type Plan struct {
	Remove []string
	Add    []Add
	Update []Update
}

func (p Plan) Empty() bool { return len(p.Remove)+len(p.Add)+len(p.Update) == 0 }

// Diff строит план перехода current → target. Порядок детерминирован (по значению).
func Diff(current, target map[string]State) Plan {
	var p Plan
	for _, v := range slices.Sorted(maps.Keys(current)) {
		cur := current[v]
		tgt, ok := target[v]
		if !ok {
			p.Remove = append(p.Remove, v)
			continue
		}
		var u Update
		if cur.Comment != tgt.Comment {
			u.Comment = &tgt.Comment
		}
		if cur.Enabled != tgt.Enabled {
			u.Enabled = &tgt.Enabled
		}
		if u.Comment != nil || u.Enabled != nil {
			u.Value = v
			p.Update = append(p.Update, u)
		}
	}
	for _, v := range slices.Sorted(maps.Keys(target)) {
		if _, ok := current[v]; !ok {
			p.Add = append(p.Add, Add{Value: v, State: target[v]})
		}
	}
	return p
}
