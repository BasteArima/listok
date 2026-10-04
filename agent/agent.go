// Package agent — файлы агента на роутере (ucode-скрипт, procd init) и установщик.
// Сервис встраивает их в бинарник и отдаёт по /install/<token>. Описание: docs/router-agent.md.
package agent

import (
	"bytes"
	_ "embed"
	"regexp"
	"strings"
	"text/template"
)

//go:embed listok-agent.uc
var Code string

//go:embed listok-agent.init
var Init string

//go:embed install.sh.tmpl
var installTmpl string

// Version — версия агента из listok-agent.uc (const VERSION). Её сообщают в hello и показывают на карточке роутера.
var Version = func() string {
	m := regexp.MustCompile(`(?m)^const VERSION = '([^']+)';`).FindStringSubmatch(Code)
	if m == nil {
		panic("agent: в listok-agent.uc нет const VERSION")
	}
	return m[1]
}()

// Feed — секция forkop и токен её фида.
type Feed struct {
	Section string
	Token   string
}

// InstallParams — то, что подставляется в установщик.
type InstallParams struct {
	Server     string // LISTOK_BASE_URL
	ServerIP   string // необязательно: адрес сервера в сети роутера (D-028)
	AgentToken string
	Feeds      []Feed
	Mode       string // wait или poll
	IntervalS  int    // для poll
}

var tmpl = template.Must(template.New("install").Delims("[[", "]]").Parse(installTmpl))

// Install собирает sh-установщик. Значения должны быть уже проверены: они попадают в одинарные кавычки sh.
func Install(p InstallParams) ([]byte, error) {
	var buf bytes.Buffer
	err := tmpl.Execute(&buf, struct {
		InstallParams
		AgentCode, InitCode string
	}{p, withNewline(Code), withNewline(Init)})
	return buf.Bytes(), err
}

// withNewline — heredoc в установщике закрывается маркером с новой строки.
func withNewline(s string) string {
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}
