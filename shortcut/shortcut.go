// Package shortcut — команда для iPad и iPhone в формате приложения «Команды» (docs/extension.md).
//
// Workflow описывает команду. listok.plist генерируется из неё (go run ./shortcut/generate) и подписывается
// на Mac (shortcut/sign.sh): без подписи iOS файл не импортирует. Подписанный файл кладётся в репозиторий,
// сервис отдаёт его на вкладке «Подключения». Адреса и токена в файле нет: их спрашивают при добавлении команды.
package shortcut

import (
	"bytes"
	"embed"
	"encoding/xml"
	"fmt"
	"sort"
	"unicode/utf16"
)

//go:embed listok*
var files embed.FS

// Signed — подписанная команда. Нет файла — команду ещё не подписывали.
func Signed() ([]byte, bool) {
	b, err := files.ReadFile("listok-signed.shortcut")
	return b, err == nil
}

// Источник формата — открытые генераторы команд (Cherri, shortcuts-js): Apple его не документирует.
const (
	action      = "is.workflow.actions."
	placeholder = "￼" // символ на месте переменной в тексте
	iconColor   = 0x3871DEFF
	iconGlyph   = 0xE835 // список
)

type dict = map[string]any

// output — результат предыдущего действия.
type output struct{ uuid, name string }

func (o output) ref() dict {
	return dict{"OutputName": o.name, "OutputUUID": o.uuid, "Type": "ActionOutput"}
}

func (o output) attachment() dict {
	return dict{"Value": o.ref(), "WFSerializationType": "WFTextTokenAttachment"}
}

// shortcutInput — то, что пришло из меню «Поделиться».
var shortcutInput = dict{"Type": "ExtensionInput"}

// text — строка с переменными: части типа string, output или dict (shortcutInput).
func text(parts ...any) dict {
	var s string
	attachments := dict{}
	pos := func() int { return len(utf16.Encode([]rune(s))) }
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			s += v
		case output:
			attachments[fmt.Sprintf("{%d, 1}", pos())] = v.ref()
			s += placeholder
		case dict:
			attachments[fmt.Sprintf("{%d, 1}", pos())] = v
			s += placeholder
		}
	}
	return dict{"Value": dict{"attachmentsByRange": attachments, "string": s}, "WFSerializationType": "WFTextTokenString"}
}

// fields — словарь параметра: заголовки или тело запроса, значения текстовые.
func fields(kv ...any) dict {
	var items []any
	for i := 0; i+1 < len(kv); i += 2 {
		items = append(items, dict{"WFItemType": 0, "WFKey": kv[i], "WFValue": kv[i+1]})
	}
	return dict{"Value": dict{"WFDictionaryFieldValueItems": items}, "WFSerializationType": "WFDictionaryFieldValue"}
}

func act(id string, out *output, params dict) dict {
	if out != nil {
		params["UUID"] = out.uuid
		params["CustomOutputName"] = out.name
	}
	return dict{"WFWorkflowActionIdentifier": action + id, "WFWorkflowActionParameters": params}
}

// Workflow — команда: спросить при добавлении строку подключения; при запуске взять списки из listok,
// предложить выбрать и отправить туда адрес страницы. Итог — уведомление. Ссылку разбирает сервер.
func Workflow() dict {
	id := func(n int) string { return fmt.Sprintf("4C15700A-0000-4000-8000-%012d", n) }
	var (
		connection = output{id(1), "Строка подключения"}
		parts      = output{id(2), "Части"}
		base       = output{id(3), "Адрес"}
		token      = output{id(4), "Токен"}
		lists      = output{id(5), "Списки"}
		menu       = output{id(6), "Меню"}
		titles     = output{id(7), "Названия"}
		title      = output{id(8), "Список"}
		slug       = output{id(9), "Адрес списка"}
		result     = output{id(10), "Ответ"}
		message    = output{id(11), "Итог"}
	)
	auth := func() dict { return fields(text("Authorization"), text("Bearer ", token)) }

	actions := []any{
		act("gettext", &connection, dict{"WFTextActionText": ""}),
		act("text.split", &parts, dict{"text": connection.attachment(), "Show-text": true,
			"WFTextSeparator": "Custom", "WFTextCustomSeparator": "#"}),
		act("getitemfromlist", &base, dict{"WFInput": parts.attachment(), "WFItemSpecifier": "First Item"}),
		act("getitemfromlist", &token, dict{"WFInput": parts.attachment(), "WFItemSpecifier": "Last Item"}),
		act("downloadurl", &lists, dict{"WFURL": text(base, "/api/v1/lists"), "WFHTTPMethod": "GET",
			"ShowHeaders": true, "WFHTTPHeaders": auth()}),
		act("getvalueforkey", &menu, dict{"WFInput": lists.attachment(), "WFDictionaryKey": "menu", "WFGetDictionaryValueType": "Value"}),
		act("getvalueforkey", &titles, dict{"WFInput": menu.attachment(), "WFGetDictionaryValueType": "All Keys"}),
		act("choosefromlist", &title, dict{"WFInput": titles.attachment(), "WFChooseFromListActionPrompt": "В какой список?"}),
		act("getvalueforkey", &slug, dict{"WFInput": menu.attachment(), "WFDictionaryKey": text(title), "WFGetDictionaryValueType": "Value"}),
		act("downloadurl", &result, dict{"WFURL": text(base, "/api/v1/lists/", slug, "/entries"), "WFHTTPMethod": "POST",
			"ShowHeaders": true, "WFHTTPHeaders": auth(), "WFHTTPBodyType": "JSON",
			"WFJSONValues": fields(text("input"), text(shortcutInput))}),
		act("getvalueforkey", &message, dict{"WFInput": result.attachment(), "WFDictionaryKey": "message", "WFGetDictionaryValueType": "Value"}),
		act("notification", nil, dict{"WFNotificationActionTitle": "listok", "WFNotificationActionBody": text(message)}),
	}

	return dict{
		"WFWorkflowActions":                    actions,
		"WFWorkflowClientVersion":              "3036.0.4.2",
		"WFWorkflowMinimumClientVersion":       900,
		"WFWorkflowMinimumClientVersionString": "900",
		"WFWorkflowIcon":                       dict{"WFWorkflowIconStartColor": iconColor, "WFWorkflowIconGlyphNumber": iconGlyph},
		"WFWorkflowTypes":                      []any{"ActionExtension"},
		"WFWorkflowInputContentItemClasses":    []any{"WFURLContentItem"},
		"WFWorkflowOutputContentItemClasses":   []any{},
		"WFWorkflowHasShortcutInputVariables":  true,
		"WFQuickActionSurfaces":                []any{},
		"WFWorkflowImportQuestions": []any{dict{
			"ActionIndex":  0,
			"Category":     "Parameter",
			"DefaultValue": "",
			"ParameterKey": "WFTextActionText",
			"Text":         "Строка подключения с вкладки «Подключения» в listok",
		}},
	}
}

// Plist — команда в XML-формате plist.
func Plist() []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
`)
	encode(&b, Workflow(), 0)
	b.WriteString("</plist>\n")
	return b.Bytes()
}

func encode(b *bytes.Buffer, v any, depth int) {
	indent := func() {
		for i := 0; i < depth; i++ {
			b.WriteByte('\t')
		}
	}
	indent()
	switch t := v.(type) {
	case string:
		b.WriteString("<string>")
		xml.EscapeText(b, []byte(t))
		b.WriteString("</string>\n")
	case int:
		fmt.Fprintf(b, "<integer>%d</integer>\n", t)
	case bool:
		fmt.Fprintf(b, "<%t/>\n", t)
	case []any:
		b.WriteString("<array>\n")
		for _, item := range t {
			encode(b, item, depth+1)
		}
		indent()
		b.WriteString("</array>\n")
	case dict:
		b.WriteString("<dict>\n")
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			indent()
			b.WriteString("\t<key>")
			xml.EscapeText(b, []byte(k))
			b.WriteString("</key>\n")
			encode(b, t[k], depth+1)
		}
		indent()
		b.WriteString("</dict>\n")
	default:
		panic(fmt.Sprintf("shortcut: неизвестный тип %T", v))
	}
}
