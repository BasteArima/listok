package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	if Version == "" || strings.ContainsAny(Version, " '\n") {
		t.Fatalf("версия агента %q", Version)
	}
}

// TestNoForwardCalls — функция агента не вызывает функцию, объявленную ниже по файлу. В ucode имя
// внутри функции связывается при компиляции: ниже объявленное ещё не видно, и под 'use strict'
// вызов падает только при исполнении («access to undeclared variable»), а ucode -c его пропускает.
func TestNoForwardCalls(t *testing.T) {
	decl := map[string]int{}
	for _, m := range regexp.MustCompile(`(?m)^function (\w+)\(`).FindAllStringSubmatchIndex(Code, -1) {
		decl[Code[m[2]:m[3]]] = m[0]
	}
	for name, at := range decl {
		for _, m := range regexp.MustCompile(`\b`+name+`\(`).FindAllStringIndex(Code[:at], -1) {
			line := strings.Count(Code[:m[0]], "\n") + 1
			t.Errorf("строка %d: вызов %s до её объявления", line, name)
		}
	}
}

func TestInstallScript(t *testing.T) {
	p := InstallParams{
		Server:     "https://listok.example.com",
		ServerIP:   "192.168.1.10",
		AgentToken: "AgentTokenAgentTokenAgentTokenAgentToken123",
		Feeds: []Feed{
			{Section: "main", Token: "MainTokenMainTokenMainTokenMainTokenMain123"},
			{Section: "geo", Token: "GeoTokenGeoTokenGeoTokenGeoTokenGeoToken123"},
		},
		Mode: "wait",
	}
	b, err := Install(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"SERVER='https://listok.example.com'",
		"SECTIONS='main geo'",
		"\toption server 'https://listok.example.com'\n\toption server_ip '192.168.1.10'\n\toption token 'AgentToken",
		"\toption mode 'wait'\n\nconfig feed",
		"\toption section 'main'\n\toption token 'MainToken",
		"\toption section 'geo'\n\toption token 'GeoToken",
		"const VERSION = '" + Version + "';",
		"\nLISTOK_AGENT_EOF\n", "\nLISTOK_INIT_EOF\n", "\nLISTOK_CONFIG_EOF\n",
		"cp /etc/config/forkop \"/etc/config/forkop.bak-listok-$STAMP\"",
		"ucode /tmp/listok/listok-agent check $SECTIONS || exit 1",
		"mv /tmp/listok/listok-agent /usr/bin/listok-agent",
		"/usr/bin/listok-agent attach $SECTIONS ||",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("в установщике нет %q", want)
		}
	}
	// Маркеры heredoc не должны встречаться внутри вставленных файлов.
	for _, m := range []string{"LISTOK_AGENT_EOF", "LISTOK_INIT_EOF", "LISTOK_CONFIG_EOF"} {
		if strings.Contains(Code, m) || strings.Contains(Init, m) {
			t.Errorf("маркер %s встречается во вставляемом файле", m)
		}
	}

	// Без IP сервера строки server_ip в конфиге нет вовсе (в коде агента это слово есть, его не смотрим).
	// Режим poll добавляет интервал.
	p.ServerIP, p.Mode, p.IntervalS = "", "poll", 600
	b, _ = Install(p)
	_, cfg, _ := strings.Cut(string(b), "cat > /etc/config/listok <<'LISTOK_CONFIG_EOF'")
	cfg, _, _ = strings.Cut(cfg, "LISTOK_CONFIG_EOF")
	if !strings.Contains(cfg, "option token 'AgentToken") || strings.Contains(cfg, "server_ip") ||
		!strings.Contains(cfg, "\toption mode 'poll'\n\toption interval '600'\n") {
		t.Errorf("конфиг без адреса сервера:\n%s", cfg)
	}

	// Синтаксис sh — если sh есть (в CI есть).
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh не найден, проверка синтаксиса пропущена")
	}
	f := filepath.Join(t.TempDir(), "install.sh")
	os.WriteFile(f, b, 0o644)
	if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	initFile := filepath.Join(t.TempDir(), "init")
	os.WriteFile(initFile, []byte(Init), 0o644)
	if out, err := exec.Command(sh, "-n", initFile).CombinedOutput(); err != nil {
		t.Fatalf("sh -n init: %v\n%s", err, out)
	}
}
