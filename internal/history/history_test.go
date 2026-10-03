package history

import (
	"maps"
	"testing"

	"github.com/BasteArima/listok/internal/store"
)

func p[T any](v T) *T { return &v }

func add(v, comment string) store.Change {
	return store.Change{Op: "add", Value: v, Kind: "domain", NewComment: p(comment), NewEnabled: p(true)}
}
func remove(v, comment string, enabled bool) store.Change {
	return store.Change{Op: "remove", Value: v, Kind: "domain", OldComment: p(comment), OldEnabled: p(enabled)}
}
func setComment(v, old, new string) store.Change {
	return store.Change{Op: "update", Value: v, Kind: "domain", OldComment: p(old), NewComment: p(new)}
}
func setEnabled(v string, old, new bool) store.Change {
	return store.Change{Op: "update", Value: v, Kind: "domain", OldEnabled: p(old), NewEnabled: p(new)}
}

// apply — прямое применение версий (от старых к новым): эталон для проверки Reconstruct.
func apply(start map[string]State, olderFirst [][]store.Change) map[string]State {
	m := maps.Clone(start)
	for _, v := range olderFirst {
		for _, c := range v {
			switch c.Op {
			case "add":
				m[c.Value] = State{Kind: c.Kind, Comment: *c.NewComment, Enabled: *c.NewEnabled}
			case "remove":
				delete(m, c.Value)
			case "update":
				st := m[c.Value]
				if c.NewComment != nil {
					st.Comment = *c.NewComment
				}
				if c.NewEnabled != nil {
					st.Enabled = *c.NewEnabled
				}
				m[c.Value] = st
			}
		}
	}
	return m
}

func TestReconstructRoundTrip(t *testing.T) {
	v1 := []store.Change{add("a.com", ""), add("b.com", "видео")}
	v2 := []store.Change{remove("a.com", "", true), setComment("b.com", "видео", "ютуб")}
	v3 := []store.Change{setEnabled("b.com", true, false), add("a.com", "снова")}                    // удалили и вернули в другой версии
	v4 := []store.Change{add("c.com", ""), setComment("c.com", "", "x"), remove("c.com", "x", true)} // внутри одной версии

	history := [][]store.Change{v1, v2, v3, v4}
	states := []map[string]State{{}}
	for i := range history {
		states = append(states, apply(states[0], history[:i+1]))
	}
	current := states[len(states)-1]

	for n := 0; n <= len(history); n++ {
		// Версии новее n, от новых к старым.
		var newer [][]store.Change
		for i := len(history) - 1; i >= n; i-- {
			newer = append(newer, history[i])
		}
		got := Reconstruct(current, newer)
		if !maps.Equal(got, states[n]) {
			t.Errorf("состояние на версию %d:\n получили %v\n хотели   %v", n, got, states[n])
		}
	}
}

func TestDiff(t *testing.T) {
	cur := map[string]State{
		"keep.com":    {Kind: "domain", Comment: "a", Enabled: true},
		"gone.com":    {Kind: "domain", Enabled: true},
		"comment.com": {Kind: "domain", Comment: "old", Enabled: true},
		"toggle.com":  {Kind: "domain", Enabled: false},
	}
	tgt := map[string]State{
		"keep.com":    {Kind: "domain", Comment: "a", Enabled: true},
		"comment.com": {Kind: "domain", Comment: "new", Enabled: true},
		"toggle.com":  {Kind: "domain", Enabled: true},
		"back.com":    {Kind: "domain", Comment: "c", Enabled: false},
	}
	plan := Diff(cur, tgt)
	if len(plan.Remove) != 1 || plan.Remove[0] != "gone.com" {
		t.Errorf("Remove = %v", plan.Remove)
	}
	if len(plan.Add) != 1 || plan.Add[0].Value != "back.com" || plan.Add[0].State.Enabled || plan.Add[0].State.Comment != "c" {
		t.Errorf("Add = %+v", plan.Add)
	}
	if len(plan.Update) != 2 {
		t.Fatalf("Update = %+v", plan.Update)
	}
	if u := plan.Update[0]; u.Value != "comment.com" || u.Comment == nil || *u.Comment != "new" || u.Enabled != nil {
		t.Errorf("Update[0] = %+v", u)
	}
	if u := plan.Update[1]; u.Value != "toggle.com" || u.Enabled == nil || !*u.Enabled || u.Comment != nil {
		t.Errorf("Update[1] = %+v", u)
	}
	if !Diff(tgt, tgt).Empty() {
		t.Error("diff одинаковых состояний должен быть пустым")
	}
}
