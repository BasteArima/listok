// Package extension — расширение браузера (docs/extension.md). Сервис встраивает его в бинарник
// и отдаёт архивом на вкладке «Подключения»: для установки не нужен ни репозиторий, ни магазин.
package extension

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"sync"
)

//go:embed manifest.json *.html *.js *.css icons
var files embed.FS

// Version — версия расширения из manifest.json.
var Version = func() string {
	b, err := files.ReadFile("manifest.json")
	if err != nil {
		panic(err)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Version == "" {
		panic("extension: в manifest.json нет version")
	}
	return m.Version
}()

// Zip — архив расширения с папкой listok-extension внутри. Собирается один раз.
var Zip = sync.OnceValues(func() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := zw.Create("listok-extension/" + path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
})
