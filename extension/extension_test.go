package extension

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestManifest(t *testing.T) {
	b, err := files.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ManifestVersion int               `json:"manifest_version"`
		Icons           map[string]string `json:"icons"`
		Permissions     []string          `json:"permissions"`
		Action          struct {
			Popup string            `json:"default_popup"`
			Icon  map[string]string `json:"default_icon"`
		} `json:"action"`
		Options struct {
			Page string `json:"page"`
		} `json:"options_ui"`
		HostPermissions []string `json:"host_permissions"`
		Background      struct {
			Worker string `json:"service_worker"`
		} `json:"background"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.ManifestVersion != 3 {
		t.Fatalf("manifest_version = %d", m.ManifestVersion)
	}
	// Файлы, на которые ссылается манифест, должны быть в архиве.
	refs := []string{m.Action.Popup, m.Options.Page}
	for _, p := range m.Icons {
		refs = append(refs, p)
	}
	for _, p := range m.Action.Icon {
		refs = append(refs, p)
	}
	for _, p := range refs {
		if _, err := files.ReadFile(p); err != nil {
			t.Errorf("манифест ссылается на %q: %v", p, err)
		}
	}
	// Запросы только наблюдаем: разрешения на их блокировку и изменение расширению не нужны (D-042).
	if strings.Join(m.Permissions, ",") != "storage,webRequest" || strings.Join(m.HostPermissions, ",") != "<all_urls>" {
		t.Fatalf("permissions = %v, host_permissions = %v", m.Permissions, m.HostPermissions)
	}
	if _, err := files.ReadFile(m.Background.Worker); err != nil {
		t.Errorf("фоновый скрипт %q: %v", m.Background.Worker, err)
	}
}

// CSP расширений MV3 запрещает inline-скрипты и внешние файлы: ошибка видна только при запуске в браузере.
func TestNoInlineOrRemoteCode(t *testing.T) {
	script := regexp.MustCompile(`<script[^>]*>`)
	for _, name := range []string{"popup.html", "options.html"} {
		b, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range script.FindAllString(string(b), -1) {
			m := regexp.MustCompile(`src="([^"]+)"`).FindStringSubmatch(tag)
			if m == nil {
				t.Errorf("%s: inline-скрипт %s", name, tag)
				continue
			}
			if strings.Contains(m[1], "://") {
				t.Errorf("%s: внешний скрипт %s", name, m[1])
				continue
			}
			if _, err := files.ReadFile(m[1]); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		if regexp.MustCompile(`\son[a-z]+=`).Match(b) {
			t.Errorf("%s: обработчик в атрибуте", name)
		}
	}
}

func TestZip(t *testing.T) {
	data, err := Zip()
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range r.File {
		names[f.Name] = true
		if strings.HasSuffix(f.Name, ".go") {
			t.Errorf("в архиве файл Go: %s", f.Name)
		}
	}
	for _, want := range []string{"manifest.json", "popup.html", "popup.js", "options.html", "options.js", "client.js", "background.js", "style.css", "icons/128.png"} {
		if !names["listok-extension/"+want] {
			t.Errorf("в архиве нет %s", want)
		}
	}
}
