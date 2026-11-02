package shortcut

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPlistUpToDate(t *testing.T) {
	onDisk, err := os.ReadFile("listok.plist")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, Plist()) {
		t.Fatal("listok.plist устарел: go run ./shortcut/generate, затем снова подписать на Mac")
	}
}

func TestPlistWellFormed(t *testing.T) {
	d := xml.NewDecoder(bytes.NewReader(Plist()))
	d.Strict = true
	for {
		_, err := d.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// В публичный файл не должны попасть адрес и токен: их спрашивают при добавлении команды.
func TestNoSecrets(t *testing.T) {
	p := string(Plist())
	for _, bad := range []string{"http://", "https://", "Bearer ey"} {
		// Единственный http — DTD в шапке plist.
		if bad == "http://" {
			p2 := strings.Replace(p, "http://www.apple.com/DTDs/PropertyList-1.0.dtd", "", 1)
			if strings.Contains(p2, bad) {
				t.Errorf("в команде есть адрес: %s", bad)
			}
			continue
		}
		if strings.Contains(p, bad) {
			t.Errorf("в команде есть %q", bad)
		}
	}
}

// Каждая переменная ссылается на результат действия, которое стоит раньше.
func TestOutputsDefinedBeforeUse(t *testing.T) {
	seen := map[string]bool{}
	uuidRe := regexp.MustCompile(`"OutputUUID":"[^"]+"`)
	actions := Workflow()["WFWorkflowActions"].([]any)
	for i, a := range actions {
		params := a.(dict)["WFWorkflowActionParameters"].(dict)
		for _, m := range uuidRe.FindAllString(flatten(params), -1) {
			id := strings.TrimSuffix(strings.TrimPrefix(m, `"OutputUUID":"`), `"`)
			if !seen[id] {
				t.Errorf("действие %d использует результат %s до его появления", i, id)
			}
		}
		if id, ok := params["UUID"].(string); ok {
			seen[id] = true
		}
	}
	if len(actions) != 12 {
		t.Errorf("действий: %d", len(actions))
	}
}

func flatten(v any) string {
	switch t := v.(type) {
	case dict:
		var b strings.Builder
		for k, val := range t {
			if k == "OutputUUID" {
				fmt.Fprintf(&b, `"OutputUUID":"%v"`, val)
				continue
			}
			b.WriteString(flatten(val))
		}
		return b.String()
	case []any:
		var b strings.Builder
		for _, item := range t {
			b.WriteString(flatten(item))
		}
		return b.String()
	}
	return ""
}

// Позиции переменных считаются в UTF-16: на них опирается «Команды».
func TestTextPositions(t *testing.T) {
	got := text("Bearer ", output{"id", "x"})["Value"].(dict)
	if got["string"] != "Bearer "+placeholder {
		t.Fatalf("строка %q", got["string"])
	}
	if _, ok := got["attachmentsByRange"].(dict)["{7, 1}"]; !ok {
		t.Fatalf("позиция %v", got["attachmentsByRange"])
	}
}
