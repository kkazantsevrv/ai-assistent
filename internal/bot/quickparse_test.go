package bot

import (
	"strings"
	"testing"
	"time"
)

func TestParseQuickRemind(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Moscow")
	now := time.Date(2026, 11, 16, 14, 0, 0, 0, loc)
	cases := []struct {
		in   string
		want string // "" — ошибка разбора
		text string
	}{
		{"30m проверить почту", "2026-11-16 14:30", "проверить почту"},
		{"1h30m забрать посылку", "2026-11-16 15:30", "забрать посылку"},
		{"2ч позвонить", "2026-11-16 16:00", "позвонить"},
		{"1д оплатить", "2026-11-17 14:00", "оплатить"},
		{"18:30 ужин", "2026-11-16 18:30", "ужин"},
		{"9:00 зарядка", "2026-11-17 09:00", "зарядка"},
		{"завтра 9:00 спортзал", "2026-11-17 09:00", "спортзал"},
		{"25.12 10:00 подарки", "2026-12-25 10:00", "подарки"},
		{"05.01 10:00 после праздников", "2027-01-05 10:00", "после праздников"},
		{"2026-12-31 23:00 новый год", "2026-12-31 23:00", "новый год"},
		{"сегодня 10:00 прошло", "", ""},
		{"позвонить маме", "", ""},
		{"30m", "", ""},
	}
	for _, c := range cases {
		at, text, ok := parseQuickRemind(c.in, now)
		if c.want == "" {
			if ok {
				t.Errorf("%q: ожидалась ошибка, получено %v %q", c.in, at, text)
			}
			continue
		}
		if !ok {
			t.Errorf("%q: не разобрано", c.in)
			continue
		}
		if got := at.Format("2006-01-02 15:04"); got != c.want || text != c.text {
			t.Errorf("%q: got %s %q, want %s %q", c.in, got, text, c.want, c.text)
		}
	}
}

func TestParseTimezone(t *testing.T) {
	cases := map[string]string{
		"Europe/Moscow": "Europe/Moscow",
		"+5":            "Etc/GMT-5",
		"UTC+3":         "Etc/GMT-3",
		"utc-4":         "Etc/GMT+4",
		"мск":           "Europe/Moscow",
		"МСК+2":         "Etc/GMT-5",
		"0":             "",
	}
	for in, want := range cases {
		got, ok := parseTimezone(in)
		if want == "" {
			if ok {
				t.Errorf("%q: ожидалась ошибка, получено %q", in, got)
			}
			continue
		}
		if got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestMarkdownToHTML(t *testing.T) {
	in := "### План\n**Важно**: купить *молоко* и `хлеб` <сегодня>\n- пункт\n```\na < b\n```\n| День | Время |\n|---|---|\n| Пн | 09:00 |\n[сайт](https://example.com/?a=1&b=2)"
	got := markdownToHTML(in)
	for _, want := range []string{
		"<b>План</b>",
		"<b>Важно</b>: купить <i>молоко</i> и <code>хлеб</code> &lt;сегодня&gt;",
		"• пункт",
		"<pre>a &lt; b</pre>",
		"<pre>День │ Время\nПн │ 09:00</pre>",
		`<a href="https://example.com/?a=1&amp;b=2">сайт</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в\n%s", want, got)
		}
	}
	if strings.Contains(markdownToHTML("snake_case_name и 2*3*4"), "<i>") {
		t.Error("ложное срабатывание курсива")
	}
}

func TestSplitMarkdownKeepsCodeBlocks(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("```\n")
	for i := 0; i < 200; i++ {
		sb.WriteString("строка кода номер такая-то\n")
	}
	sb.WriteString("```\nконец")
	parts := splitMarkdown(sb.String(), 1000)
	if len(parts) < 2 {
		t.Fatal("ожидалось несколько частей")
	}
	for i, p := range parts {
		if strings.Count(p, "```")%2 != 0 {
			t.Errorf("часть %d с незакрытым блоком кода", i)
		}
		if len([]rune(p)) > 1010 {
			t.Errorf("часть %d слишком длинная: %d", i, len([]rune(p)))
		}
	}
}
