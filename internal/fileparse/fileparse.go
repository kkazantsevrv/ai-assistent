// Package fileparse извлекает текст из присланных файлов: txt/md/csv,
// Word (.docx) и Excel (.xlsx). Результат — обычный текст, который потом
// отдаётся языковой модели.
package fileparse

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// ErrUnsupported — формат не поддерживается.
var ErrUnsupported = errors.New("формат файла не поддерживается")

// LegacyError — старый двоичный формат Office (.doc/.xls).
type LegacyError struct{ Ext, Suggest string }

func (e *LegacyError) Error() string {
	return fmt.Sprintf("старый формат %s не поддерживается — пересохраните файл как %s", e.Ext, e.Suggest)
}

var textExts = map[string]bool{
	".txt": true, ".text": true, ".md": true, ".markdown": true, ".log": true,
	".json": true, ".xml": true, ".ics": true, ".ical": true, ".html": true, ".htm": true,
	".yaml": true, ".yml": true, ".ini": true, ".srt": true,
}

// Extract определяет тип файла по расширению/MIME и возвращает его текст.
func Extract(filename, mime string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case ext == ".docx" || ext == ".docm" || ext == ".dotx" || strings.Contains(mime, "wordprocessingml"):
		return ExtractDOCX(data)
	case ext == ".xlsx" || ext == ".xlsm" || ext == ".xltx" || strings.Contains(mime, "spreadsheetml"):
		return ExtractXLSX(data)
	case ext == ".csv" || ext == ".tsv" || mime == "text/csv":
		return extractCSV(data, ext == ".tsv")
	case ext == ".doc":
		return "", &LegacyError{Ext: ".doc", Suggest: ".docx"}
	case ext == ".xls":
		return "", &LegacyError{Ext: ".xls", Suggest: ".xlsx"}
	case textExts[ext] || strings.HasPrefix(mime, "text/"):
		return DecodeText(data), nil
	}
	// Неизвестное расширение: если похоже на текст — читаем как текст.
	if looksLikeText(data) {
		return DecodeText(data), nil
	}
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		// ZIP без понятного расширения: пробуем Word, затем Excel.
		if s, err := ExtractDOCX(data); err == nil && strings.TrimSpace(s) != "" {
			return s, nil
		}
		if s, err := ExtractXLSX(data); err == nil && strings.TrimSpace(s) != "" {
			return s, nil
		}
	}
	return "", ErrUnsupported
}

func looksLikeText(data []byte) bool {
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.HasPrefix(sample, []byte{0xFF, 0xFE}) || bytes.HasPrefix(sample, []byte{0xFE, 0xFF}) {
		return true
	}
	return len(sample) > 0 && !bytes.ContainsRune(sample, 0)
}

// DecodeText приводит текст к UTF-8: понимает BOM, UTF-16 и Windows-1251
// (типичная кодировка «Блокнота» и CSV из русского Excel).
func DecodeText(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		data = data[3:]
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		if out, err := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder().Bytes(data); err == nil {
			return normalizeNewlines(string(out))
		}
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		if out, err := unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder().Bytes(data); err == nil {
			return normalizeNewlines(string(out))
		}
	}
	if !utf8.Valid(data) {
		if out, err := charmap.Windows1251.NewDecoder().Bytes(data); err == nil {
			return normalizeNewlines(string(out))
		}
	}
	return normalizeNewlines(string(data))
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func extractCSV(data []byte, tsv bool) (string, error) {
	text := DecodeText(data)
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	switch {
	case tsv:
		r.Comma = '\t'
	default:
		// Русский Excel сохраняет CSV через «;».
		first, _, _ := strings.Cut(text, "\n")
		if strings.Count(first, ";") > strings.Count(first, ",") {
			r.Comma = ';'
		} else if strings.Count(first, "\t") > strings.Count(first, ",") {
			r.Comma = '\t'
		}
	}
	rows, err := r.ReadAll()
	if err != nil {
		// Не получилось как CSV — отдаём как есть.
		return text, nil
	}
	var b strings.Builder
	for _, row := range rows {
		writeRow(&b, row)
	}
	return b.String(), nil
}

// writeRow пишет строку таблицы через « | », пропуская пустые строки.
func writeRow(b *strings.Builder, cells []string) {
	last := -1
	for i, c := range cells {
		cells[i] = strings.Join(strings.Fields(c), " ")
		if cells[i] != "" {
			last = i
		}
	}
	if last < 0 {
		return
	}
	b.WriteString(strings.Join(cells[:last+1], " | "))
	b.WriteByte('\n')
}
