package fileparse

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// ExtractDOCX достаёт текст из документа Word: абзацы — строками,
// таблицы — строками с ячейками через « | ».
func ExtractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("не удалось открыть .docx (файл повреждён?)")
	}
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
			break
		}
	}
	if doc == nil {
		return "", errors.New("в .docx нет word/document.xml")
	}
	rc, err := doc.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return parseDocumentXML(io.LimitReader(rc, 64<<20))
}

func parseDocumentXML(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var out strings.Builder

	// Стек таблиц: для каждой — текущая строка (ячейки) и буфер ячейки.
	type tableState struct {
		row  []string
		cell strings.Builder
	}
	var tables []*tableState
	inText := false

	write := func(s string) {
		if n := len(tables); n > 0 {
			tables[n-1].cell.WriteString(s)
		} else {
			out.WriteString(s)
		}
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("не удалось разобрать .docx")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				write("\t")
			case "br", "cr":
				write("\n")
			case "tbl":
				tables = append(tables, &tableState{})
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if len(tables) > 0 {
					write(" ")
				} else {
					out.WriteByte('\n')
				}
			case "tc":
				if n := len(tables); n > 0 {
					ts := tables[n-1]
					ts.row = append(ts.row, ts.cell.String())
					ts.cell.Reset()
				}
			case "tr":
				if n := len(tables); n > 0 {
					ts := tables[n-1]
					var b strings.Builder
					writeRow(&b, ts.row)
					ts.row = nil
					if n > 1 {
						// Вложенная таблица — дописываем в ячейку внешней.
						tables[n-2].cell.WriteString(strings.TrimSpace(b.String()) + "; ")
					} else {
						out.WriteString(b.String())
					}
				}
			case "tbl":
				if n := len(tables); n > 0 {
					tables = tables[:n-1]
					if n == 1 {
						out.WriteByte('\n')
					}
				}
			}
		case xml.CharData:
			if inText {
				write(string(t))
			}
		}
	}
	return collapseBlankLines(out.String()), nil
}

func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
