package fileparse

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

const maxRowsPerSheet = 3000

// ExtractXLSX превращает книгу Excel в текст: по листам, строки — через « | ».
// Значения объединённых ячеек размножаются на всю область — в расписаниях
// так обычно оформляют день недели или время пары.
func ExtractXLSX(data []byte) (string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{
		ShortDatePattern: "dd.mm.yyyy",
		LongDatePattern:  "dd.mm.yyyy",
		LongTimePattern:  "hh:mm",
	})
	if err != nil {
		return "", errors.New("не удалось открыть .xlsx (файл повреждён или защищён паролем?)")
	}
	defer f.Close()

	var b strings.Builder
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		fillMerged(f, sheet, &rows)

		var sb strings.Builder
		for i, row := range rows {
			if i >= maxRowsPerSheet {
				fmt.Fprintf(&sb, "... (обрезано, всего строк: %d)\n", len(rows))
				break
			}
			writeRow(&sb, row)
		}
		if sb.Len() == 0 {
			continue
		}
		fmt.Fprintf(&b, "=== Лист «%s» ===\n%s\n", sheet, sb.String())
	}
	return strings.TrimSpace(b.String()), nil
}

func fillMerged(f *excelize.File, sheet string, rows *[][]string) {
	merged, err := f.GetMergeCells(sheet)
	if err != nil {
		return
	}
	for _, m := range merged {
		val := m.GetCellValue()
		if val == "" {
			continue
		}
		c1, r1, err1 := excelize.CellNameToCoordinates(m.GetStartAxis())
		c2, r2, err2 := excelize.CellNameToCoordinates(m.GetEndAxis())
		if err1 != nil || err2 != nil || r2 > maxRowsPerSheet || c2 > 200 {
			continue
		}
		for r := r1; r <= r2; r++ {
			for c := c1; c <= c2; c++ {
				setCell(rows, r-1, c-1, val)
			}
		}
	}
}

func setCell(rows *[][]string, r, c int, val string) {
	for len(*rows) <= r {
		*rows = append(*rows, nil)
	}
	row := (*rows)[r]
	for len(row) <= c {
		row = append(row, "")
	}
	if row[c] == "" {
		row[c] = val
	}
	(*rows)[r] = row
}
