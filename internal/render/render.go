// Package render turns backend responses into what an agent reads well: a
// few header lines and tab-separated tables, with numbers at fixed decimals.
//
// Every tool result goes into the agent's context, and Claude Code cuts tool
// output off at about 25k tokens. JSON spends most of that on repeated keys,
// so a table carries several times the rows in the same budget, and the
// renderer stops before the budget runs out rather than letting the client
// truncate mid-row.
package render

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// MaxBytes is the most one tool result may hold, about 15k tokens. It leaves
// room under the client's cap for the agent's own reasoning around it.
const MaxBytes = 60_000

// Doc builds one tool result.
type Doc struct {
	b   strings.Builder
	max int
	cut bool
}

// New returns a document capped at MaxBytes.
func New() *Doc { return &Doc{max: MaxBytes} }

// NewCapped returns a document capped at max bytes, for tests.
func NewCapped(max int) *Doc { return &Doc{max: max} }

// Line adds one line of prose or key figures.
func (d *Doc) Line(format string, args ...any) {
	if d.cut {
		return
	}
	d.b.WriteString(fmt.Sprintf(format, args...))
	d.b.WriteByte('\n')
}

// Blank adds an empty line between sections.
func (d *Doc) Blank() {
	if !d.cut {
		d.b.WriteByte('\n')
	}
}

// Table adds a titled table. Rows that would take the document past its cap
// are left out and counted, and the last line says how to get them: rest
// gets how many rows were shown and returns e.g. "use offset=200 or save_to";
// nil says nothing more.
func (d *Doc) Table(title string, cols []string, rows [][]string, rest func(shown int) string) {
	if d.cut {
		return
	}
	if title != "" {
		d.b.WriteString("## " + title + "\n")
	}
	d.b.WriteString(row(cols))
	for i, r := range rows {
		line := row(r)
		// Reserve room for the closing note.
		if d.b.Len()+len(line)+120 > d.max {
			d.b.WriteString(fmt.Sprintf("… %d more rows not shown", len(rows)-i))
			if rest != nil {
				if hint := rest(i); hint != "" {
					d.b.WriteString(": " + hint)
				}
			}
			d.b.WriteByte('\n')
			d.cut = true
			return
		}
		d.b.WriteString(line)
	}
}

// Truncated reports whether a table was cut short.
func (d *Doc) Truncated() bool { return d.cut }

func (d *Doc) String() string { return d.b.String() }

func row(cells []string) string {
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(Cell(c))
	}
	b.WriteByte('\n')
	return b.String()
}

// Cell makes text safe for a TSV cell: no tabs, no line breaks.
func Cell(s string) string {
	if !strings.ContainsAny(s, "\t\r\n") {
		return s
	}
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(s)
}

// Dash stands for a value that does not exist, as opposed to zero.
const Dash = "—"

// Num formats v with dp decimals; never "-0".
func Num(v float64, dp int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return Dash
	}
	s := strconv.FormatFloat(v, 'f', dp, 64)
	if strings.TrimLeft(s, "-0.") == "" {
		s = strings.TrimPrefix(s, "-")
	}
	return s
}

// Px formats a price, size or per-row amount to 8 significant digits with no
// exponent, so 0.0000012 and 65432.1 both read as written. Fixed decimals
// would turn a low-priced coin's prices and a small account's fees into 0.00.
func Px(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return Dash
	}
	if v == 0 {
		return "0"
	}
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 8, 64), 64)
	return strconv.FormatFloat(r, 'f', -1, 64)
}

// OptPx formats an optional price; nil is Dash.
func OptPx(v *float64) string {
	if v == nil {
		return Dash
	}
	return Px(*v)
}

// Opt formats an optional value; nil is Dash.
func Opt(v *float64, dp int) string {
	if v == nil {
		return Dash
	}
	return Num(*v, dp)
}

// Signed formats v with a leading + for gains.
func Signed(v float64, dp int) string {
	s := Num(v, dp)
	if v > 0 && s != Dash && strings.TrimLeft(s, "0.") != "" {
		return "+" + s
	}
	return s
}

// Pct formats a percentage that is already in percent units.
func Pct(v float64, dp int) string { return Num(v, dp) + "%" }

// OptPct formats an optional percentage; nil is Dash.
func OptPct(v *float64, dp int) string {
	if v == nil {
		return Dash
	}
	return Pct(*v, dp)
}

// Int formats an integer.
func Int(v int) string { return strconv.Itoa(v) }

// Time formats an instant as UTC minutes: 2026-10-22 14:05.
func Time(t time.Time) string {
	if t.IsZero() {
		return Dash
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// OptTime formats an optional instant.
func OptTime(t *time.Time) string {
	if t == nil {
		return Dash
	}
	return Time(*t)
}

// Date formats an instant as a UTC day.
func Date(t time.Time) string {
	if t.IsZero() {
		return Dash
	}
	return t.UTC().Format("2006-01-02")
}

// Short is the first 8 characters of an id, enough to tell sessions apart in
// prose; tables carry the whole id.
func Short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Clip shortens free text to n runes, marking the cut.
func Clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
