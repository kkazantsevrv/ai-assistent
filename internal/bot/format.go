package bot

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kkazantsevrv/ai-assistent/internal/storage"
)

// ---------- даты и время по-русски ----------

var (
	wdShort  = []string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}
	wdFull   = []string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}
	monthGen = []string{"", "января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
)

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// dayLabel — «сегодня», «завтра» или «вт, 17 ноября [2027]».
func dayLabel(t, now time.Time) string {
	switch {
	case sameDay(t, now):
		return "сегодня, " + wdShort[t.Weekday()] + " " + fmt.Sprintf("%d %s", t.Day(), monthGen[t.Month()])
	case sameDay(t, now.AddDate(0, 0, 1)):
		return "завтра, " + wdShort[t.Weekday()] + " " + fmt.Sprintf("%d %s", t.Day(), monthGen[t.Month()])
	}
	s := fmt.Sprintf("%s, %d %s", wdShort[t.Weekday()], t.Day(), monthGen[t.Month()])
	if t.Year() != now.Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s
}

// whenLabel — «сегодня 09:00», «завтра 18:30», «пт, 20 ноября 10:00».
func whenLabel(t, now time.Time) string {
	switch {
	case sameDay(t, now):
		return "сегодня " + t.Format("15:04")
	case sameDay(t, now.AddDate(0, 0, 1)):
		return "завтра " + t.Format("15:04")
	}
	s := fmt.Sprintf("%s, %d %s", wdShort[t.Weekday()], t.Day(), monthGen[t.Month()])
	if t.Year() != now.Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s + " " + t.Format("15:04")
}

// humanDuration — «15 мин», «1 ч 30 мин», «2 дн 3 ч».
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	mins := int((d + 30*time.Second) / time.Minute)
	if mins < 1 {
		return "меньше минуты"
	}
	days, hours, m := mins/1440, mins%1440/60, mins%60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d дн", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d ч", hours))
	}
	if m > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%d мин", m))
	}
	return strings.Join(parts, " ")
}

func offsetLabel(off int) string {
	if off == 0 {
		return "в момент события"
	}
	return "за " + humanDuration(time.Duration(off)*time.Minute)
}

func offsetsLabel(offs []int) string {
	var parts []string
	for _, o := range offs {
		parts = append(parts, offsetLabel(o))
	}
	return strings.Join(parts, ", ")
}

func repeatLabel(r *storage.Reminder, loc *time.Location) string {
	iv := max(r.Interval, 1)
	switch r.Repeat {
	case storage.RepeatDaily:
		if iv == 1 {
			return "каждый день"
		}
		return fmt.Sprintf("каждые %d дн.", iv)
	case storage.RepeatWeekly:
		wd := wdShort[r.Start.In(loc).Weekday()]
		if iv == 1 {
			return "каждую неделю (" + wd + ")"
		}
		return fmt.Sprintf("каждые %d нед. (%s)", iv, wd)
	case storage.RepeatWeekdays:
		return "по будням"
	case storage.RepeatMonthly:
		if iv == 1 {
			return fmt.Sprintf("каждый месяц (%d-го)", r.Start.In(loc).Day())
		}
		return fmt.Sprintf("каждые %d мес.", iv)
	case storage.RepeatYearly:
		return "каждый год"
	}
	return ""
}

// ---------- HTML для Telegram ----------

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func esc(s string) string { return htmlEscaper.Replace(s) }

var (
	reHeading = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.+?)\s*#*\s*$`)
	reBullet  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	reQuote   = regexp.MustCompile(`^\s*>\s?(.*)$`)
	reHR      = regexp.MustCompile(`^\s*([-*_]\s*){3,}$`)
	reLink    = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)"<>]+)\)`)
	reBold    = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	reBold2   = regexp.MustCompile(`__([^_\n]+?)__`)
	reStrike  = regexp.MustCompile(`~~([^~\n]+?)~~`)
	reItalic  = regexp.MustCompile(`(^|[^\w*])\*([^*\s][^*\n]*?)\*($|[^\w*])`)
	reTableSp = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)
)

// markdownToHTML переводит типичный Markdown ответа модели в HTML,
// который понимает Telegram (b, i, s, code, pre, a).
func markdownToHTML(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out strings.Builder
	var code, table []string
	inCode := false

	flushTable := func() {
		if len(table) == 0 {
			return
		}
		var rows []string
		for _, l := range table {
			if reTableSp.MatchString(strings.TrimSpace(l)) {
				continue
			}
			l = strings.TrimSpace(l)
			l = strings.TrimPrefix(l, "|")
			l = strings.TrimSuffix(l, "|")
			cells := strings.Split(l, "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(strings.ReplaceAll(cells[i], "**", ""))
			}
			rows = append(rows, strings.Join(cells, " │ "))
		}
		out.WriteString("<pre>" + esc(strings.Join(rows, "\n")) + "</pre>\n")
		table = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flushTable()
			if !inCode {
				inCode = true
				code = code[:0]
				continue
			}
			out.WriteString("<pre>" + esc(strings.Join(code, "\n")) + "</pre>\n")
			inCode = false
			continue
		}
		if inCode {
			code = append(code, line)
			continue
		}
		if strings.HasPrefix(trimmed, "|") && strings.Count(trimmed, "|") >= 2 {
			table = append(table, line)
			continue
		}
		flushTable()

		switch {
		case reHR.MatchString(line) && trimmed != "":
			out.WriteString("──────────")
		case reHeading.MatchString(line):
			m := reHeading.FindStringSubmatch(line)
			out.WriteString("<b>" + inlineMD(strings.ReplaceAll(m[1], "**", "")) + "</b>")
		case reBullet.MatchString(line):
			m := reBullet.FindStringSubmatch(line)
			out.WriteString(m[1] + "• " + inlineMD(m[2]))
		case reQuote.MatchString(line):
			m := reQuote.FindStringSubmatch(line)
			out.WriteString("┃ " + inlineMD(m[1]))
		default:
			out.WriteString(inlineMD(line))
		}
		out.WriteByte('\n')
	}
	if inCode {
		out.WriteString("<pre>" + esc(strings.Join(code, "\n")) + "</pre>\n")
	}
	flushTable()
	return strings.TrimRight(out.String(), "\n")
}

// inlineMD обрабатывает `код`, ссылки, жирный, курсив и зачёркнутый текст.
func inlineMD(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 {
		// Непарный backtick — не считаем его разметкой.
		parts = []string{s}
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + esc(p) + "</code>")
			continue
		}
		p = esc(p)
		p = reLink.ReplaceAllString(p, `<a href="$2">$1</a>`)
		p = reBold.ReplaceAllString(p, "<b>$1</b>")
		p = reBold2.ReplaceAllString(p, "<b>$1</b>")
		p = reStrike.ReplaceAllString(p, "<s>$1</s>")
		p = reItalic.ReplaceAllString(p, "$1<i>$2</i>$3")
		b.WriteString(p)
	}
	return b.String()
}

// splitMarkdown режет длинный текст на части по строкам, не разрывая блоки кода.
func splitMarkdown(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	curLen := 0
	inCode := false

	flush := func() {
		if curLen == 0 {
			return
		}
		s := cur.String()
		if inCode {
			s += "\n```"
		}
		chunks = append(chunks, strings.TrimRight(s, "\n"))
		cur.Reset()
		curLen = 0
		if inCode {
			cur.WriteString("```\n")
			curLen = 4
		}
	}

	for _, line := range strings.Split(text, "\n") {
		for utf8.RuneCountInString(line) > limit {
			r := []rune(line)
			flush()
			cur.WriteString(string(r[:limit]))
			curLen = limit
			flush()
			line = string(r[limit:])
		}
		n := utf8.RuneCountInString(line) + 1
		if curLen+n > limit {
			flush()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		curLen += n
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
		}
	}
	if curLen > 0 {
		inCode = false
		flush()
	}
	return chunks
}

// splitLines режет готовый HTML по строкам (каждая строка — законченная разметка).
func splitLines(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	n := 0
	for _, line := range strings.Split(text, "\n") {
		l := utf8.RuneCountInString(line) + 1
		if n+l > limit && n > 0 {
			chunks = append(chunks, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
			n = 0
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		n += l
	}
	if n > 0 {
		chunks = append(chunks, strings.TrimRight(cur.String(), "\n"))
	}
	return chunks
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
