package bot

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reDurPart = regexp.MustCompile(`^(\d+)\s*(дн|д|d|ч|h|мин|min|м|m)(?:\s+|$|(\d))`)
	reClock   = regexp.MustCompile(`^(\d{1,2})[:.](\d{2})(?:\s+|$)`)
	reDateDM  = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})(?:\.(\d{2,4}))?\s+`)
	reDateISO = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})[ T]+`)
)

// parseQuickRemind разбирает «30m текст», «1h30m текст», «18:30 текст»,
// «завтра 9:00 текст», «25.12 10:00 текст», «2026-12-25 10:00 текст» без ИИ.
func parseQuickRemind(s string, now time.Time) (time.Time, string, bool) {
	s = strings.TrimSpace(s)
	loc := now.Location()

	// 1) Относительное время: 30m, 2h, 1h30m, 1д.
	if d, rest, ok := parseDuration(s); ok {
		rest = strings.TrimSpace(rest)
		if rest == "" || d <= 0 {
			return time.Time{}, "", false
		}
		return now.Add(d).Truncate(time.Second), rest, true
	}

	// 2) Дата (необязательно) + время.
	day := dayStart(now)
	explicitDay := false
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "послезавтра "):
		day, s, explicitDay = day.AddDate(0, 0, 2), s[len("послезавтра "):], true
	case strings.HasPrefix(lower, "завтра "):
		day, s, explicitDay = day.AddDate(0, 0, 1), s[len("завтра "):], true
	case strings.HasPrefix(lower, "сегодня "):
		s, explicitDay = s[len("сегодня "):], true
	default:
		if m := reDateISO.FindStringSubmatch(s); m != nil {
			y, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			dd, _ := strconv.Atoi(m[3])
			day = time.Date(y, time.Month(mo), dd, 0, 0, 0, 0, loc)
			s, explicitDay = s[len(m[0]):], true
		} else if m := reDateDM.FindStringSubmatch(s); m != nil {
			dd, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			y := now.Year()
			if m[3] != "" {
				y, _ = strconv.Atoi(m[3])
				if y < 100 {
					y += 2000
				}
			}
			if mo < 1 || mo > 12 || dd < 1 || dd > 31 {
				return time.Time{}, "", false
			}
			day = time.Date(y, time.Month(mo), dd, 0, 0, 0, 0, loc)
			if m[3] == "" && day.Before(dayStart(now)) {
				day = day.AddDate(1, 0, 0) // 05.01 в декабре — это следующий год
			}
			s, explicitDay = s[len(m[0]):], true
		}
	}

	m := reClock.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, "", false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	if h > 23 || mi > 59 {
		return time.Time{}, "", false
	}
	text := strings.TrimSpace(s[len(m[0]):])
	if text == "" {
		return time.Time{}, "", false
	}
	at := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, loc)
	if !at.After(now) {
		if explicitDay {
			return time.Time{}, "", false
		}
		at = at.AddDate(0, 0, 1) // «18:30», а уже 19:00 — значит завтра
	}
	return at, text, true
}

func parseDuration(s string) (time.Duration, string, bool) {
	var total time.Duration
	matched := false
	for {
		m := reDurPart.FindStringSubmatchIndex(s)
		if m == nil {
			break
		}
		n, _ := strconv.Atoi(s[m[2]:m[3]])
		unit := s[m[4]:m[5]]
		switch unit {
		case "дн", "д", "d":
			total += time.Duration(n) * 24 * time.Hour
		case "ч", "h":
			total += time.Duration(n) * time.Hour
		default:
			total += time.Duration(n) * time.Minute
		}
		matched = true
		if m[6] >= 0 {
			s = s[m[6]:] // следующая часть начинается сразу с цифры: 1h30m
		} else {
			s = s[m[1]:]
		}
	}
	return total, s, matched
}
