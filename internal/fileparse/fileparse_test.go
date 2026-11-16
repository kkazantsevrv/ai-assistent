package fileparse

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
)

func TestDecodeCP1251(t *testing.T) {
	src := "Понедельник 9:00 Математика"
	enc, _ := charmap.Windows1251.NewEncoder().Bytes([]byte(src))
	if got := DecodeText(enc); got != src {
		t.Fatalf("got %q", got)
	}
}

func TestCSVSemicolon(t *testing.T) {
	got, err := Extract("a.csv", "", []byte("День;Время;Предмет\r\nПн;09:00;Физика\r\n;;\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := "День | Время | Предмет\nПн | 09:00 | Физика\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestDOCX(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Расписание</w:t></w:r><w:r><w:t xml:space="preserve"> на неделю</w:t></w:r></w:p>
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>Пн</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>09:00</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>Вт</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>10:30</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
<w:p><w:r><w:t>Конец</w:t></w:r></w:p>
</w:body></w:document>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	w.Write([]byte(doc))
	zw.Close()

	got, err := Extract("s.docx", "", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := "Расписание на неделю\nПн | 09:00\nВт | 10:30\n\nКонец"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestXLSX(t *testing.T) {
	f := excelize.NewFile()
	sh := "Sheet1"
	f.SetCellValue(sh, "A1", "Дата")
	f.SetCellValue(sh, "B1", "Время")
	f.SetCellValue(sh, "C1", "Событие")
	f.SetCellValue(sh, "A2", time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC))
	f.SetCellValue(sh, "B2", "09:00")
	f.SetCellValue(sh, "C2", "Встреча")
	f.SetCellValue(sh, "C3", "Обед")
	f.MergeCell(sh, "A2", "A3")
	style, _ := f.NewStyle(&excelize.Style{NumFmt: 14})
	f.SetCellStyle(sh, "A2", "A2", style)
	tstyle, _ := f.NewStyle(&excelize.Style{NumFmt: 20}) // h:mm
	f.SetCellValue(sh, "B3", 0.5)
	f.SetCellStyle(sh, "B3", "B3", tstyle)
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}

	got, err := Extract("s.xlsx", "", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", got)
	if !strings.Contains(got, "17.11.2026 | 09:00 | Встреча") {
		t.Errorf("нет строки с датой: %q", got)
	}
	if !strings.Contains(got, "17.11.2026 | 12:00 | Обед") {
		t.Errorf("объединённая ячейка или время не размножены: %q", got)
	}
}

func TestLegacy(t *testing.T) {
	if _, err := Extract("a.xls", "", []byte{0xD0, 0xCF}); err == nil {
		t.Fatal("ожидалась ошибка")
	}
}
