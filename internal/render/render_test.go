package render

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNumbers(t *testing.T) {
	cases := map[string]string{
		Num(1.005, 2):          "1.00",
		Num(-0.0001, 2):        "0.00",
		Num(-1.5, 1):           "-1.5",
		Signed(2.5, 2):         "+2.50",
		Signed(-2.5, 2):        "-2.50",
		Signed(0.0001, 2):      "0.00",
		Pct(12.345, 1):         "12.3%",
		Opt(nil, 2):            Dash,
		OptPct(nil, 1):         Dash,
		Int(42):                "42",
		Short("0123456789ab"):  "01234567",
		Clip("héllo world", 5): "héllo…",
		Px(0.0000012345678912): "0.0000012345679",
		Px(65432.123456789):    "65432.123",
		Px(-0.5):               "-0.5",
		Px(0):                  "0",
		OptPx(nil):             Dash,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if got := Time(time.Date(2026, 10, 22, 14, 5, 9, 0, time.FixedZone("x", 4*3600))); got != "2026-10-22 10:05" {
		t.Errorf("Time = %q, want UTC minutes", got)
	}
}

func TestCellsNeverBreakTheTable(t *testing.T) {
	d := New()
	d.Table("", []string{"a", "b"}, [][]string{{"x\ty", "line\nbreak"}}, nil)
	lines := strings.Split(strings.TrimSpace(d.String()), "\n")
	if len(lines) != 2 || strings.Count(lines[1], "\t") != 1 {
		t.Fatalf("table broken by its cells: %q", d.String())
	}
}

func TestTableStopsAtTheCapAndSaysHow(t *testing.T) {
	d := NewCapped(2_000)
	d.Line("header")
	var rows [][]string
	for i := range 500 {
		rows = append(rows, []string{fmt.Sprint(i), strings.Repeat("x", 20)})
	}
	var shown int
	d.Table("rows", []string{"i", "v"}, rows, func(n int) string {
		shown = n
		return fmt.Sprintf("use offset=%d or save_to", n)
	})
	out := d.String()
	if len(out) > 2_000 {
		t.Fatalf("output %d bytes over the 2000 cap", len(out))
	}
	if !d.Truncated() || !strings.Contains(out, fmt.Sprintf("%d more rows not shown: use offset=%d or save_to", 500-shown, shown)) {
		t.Fatalf("no closing note: %q", out[len(out)-80:])
	}
	// The rows shown are the first ones, so the offset in the note is exact.
	if !strings.Contains(out, fmt.Sprintf("\n%d\t", shown-1)) || strings.Contains(out, fmt.Sprintf("\n%d\t", shown)) {
		t.Fatalf("note offset %d does not match the rows shown", shown)
	}
	// Nothing is added after a cut, so the note stays last.
	d.Line("later")
	d.Table("more", []string{"a"}, [][]string{{"b"}}, nil)
	if strings.Contains(d.String(), "later") {
		t.Fatal("wrote past the cut")
	}
}
