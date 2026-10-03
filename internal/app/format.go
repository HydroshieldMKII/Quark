package app

import (
	"time"

	"example.org/quark/internal/i18n"

	"example.org/quark/internal/pdcli"
)

// FormatTime formats a timestamp for file lists, relative to now in now's
// location: "Today 14:05", "Yesterday 09:12", "Monday 14:05" within a week,
// "3 Oct" this year, "3 Oct 2025" otherwise. The zero time is "—".
func FormatTime(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	t = t.In(now.Location())
	day := func(x time.Time) time.Time { y, m, d := x.Date(); return time.Date(y, m, d, 0, 0, 0, 0, x.Location()) }
	days := int(day(now).Sub(day(t)).Hours() / 24)
	switch {
	case days == 0:
		return Tf("Today %s", t.Format("15:04"))
	case days == 1:
		return Tf("Yesterday %s", t.Format("15:04"))
	case days > 1 && days < 7:
		return T(weekdays[t.Weekday()]) + " " + t.Format("15:04")
	case t.Year() == now.Year():
		return Tf("%d %s", t.Day(), T(months[t.Month()-1]))
	default:
		return Tf("%d %s %d", t.Day(), T(months[t.Month()-1]), t.Year())
	}
}

// Weekday and month names, marked for translation.
var (
	weekdays = [7]string{
		i18n.M("Sunday"), i18n.M("Monday"), i18n.M("Tuesday"), i18n.M("Wednesday"),
		i18n.M("Thursday"), i18n.M("Friday"), i18n.M("Saturday"),
	}
	months = [12]string{
		i18n.M("Jan"), i18n.M("Feb"), i18n.M("Mar"), i18n.M("Apr"), i18n.M("May"), i18n.M("Jun"),
		i18n.M("Jul"), i18n.M("Aug"), i18n.M("Sep"), i18n.M("Oct"), i18n.M("Nov"), i18n.M("Dec"),
	}
)

// DescribeKind describes a stored error (kind name and CLI message), e.g. a
// failed transfer restored from disk.
func DescribeKind(kind, message string) *UserError {
	if kind == "" {
		return nil
	}
	return Describe(&pdcli.Error{Kind: pdcli.ParseKind(kind), Message: message})
}
