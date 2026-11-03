package history

import (
	"strings"
	"testing"
)

func TestShiftDecimal(t *testing.T) {
	for _, c := range []struct {
		in       string
		decimals int
		want     string
	}{
		{"100000000000000", 18, "0.0001"},
		{"9900000000000000", 18, "0.0099"},
		{"630636599384776", 18, "0.000630636599384776"},
		{"-332996", 6, "-0.332996"},
		{"-32966695", 6, "-32.966695"},
		{"1000000000000000000", 18, "1"},
		{"2500000000000000000", 18, "2.5"},
		{"1", 18, "0.000000000000000001"},
		{"0", 18, "0"},
		{"-0", 6, "0"},
		{"000100", 6, "0.0001"},
		{"1234567890123456789012", 18, "1234.567890123456789012"},
		{"42048590081052", 18, "0.000042048590081052"},
		{"123", 0, "123"},
		{"-123", 0, "-123"},
	} {
		got, err := ShiftDecimal(c.in, c.decimals)
		if err != nil {
			t.Errorf("ShiftDecimal(%q,%d): %v", c.in, c.decimals, err)
			continue
		}
		if got != c.want {
			t.Errorf("ShiftDecimal(%q,%d) = %q, want %q", c.in, c.decimals, got, c.want)
		}
	}
}

// Full precision matters: raw amount0 runs to 22 digits, so parsing the
// integer into a float64 first would round before the divide.
func TestShiftDecimalBeatsFloatRoundTrip(t *testing.T) {
	const raw = "1234567890123456789012" // 22 digits, > 2^53
	got, err := ShiftDecimal(raw, 18)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1234.567890123456789012"; got != want {
		t.Fatalf("got %q, want %q (all digits preserved)", got, want)
	}
}

func TestShiftDecimalRejectsNonInteger(t *testing.T) {
	for _, in := range []string{"", "1.5", "abc", "1e18", "0x10", "-", " 12 3"} {
		if _, err := ShiftDecimal(in, 18); err == nil {
			t.Errorf("ShiftDecimal(%q) should fail", in)
		}
	}
}

func TestConvertRawCSV(t *testing.T) {
	raw := strings.Join([]string{
		"amount0,amount1,blockTimestamp,liquidity,sqrtPriceX96,tick,price",
		"100000000000000,-332996,1768753965,2639910152900894115,4573075199760799433668080,-195208,3331.635787384446",
		"-404633371653689000,1000000000,1776243967,1175758378896130813,3821062820594606087570255,-198802,2325.9972029162896",
		"bogus,1,1768753966,1,1,0,1",
	}, "\n") + "\n"

	var out strings.Builder
	st, err := ConvertRawCSV(strings.NewReader(raw), &out, BaseETHUSDC)
	if err != nil {
		t.Fatal(err)
	}
	if st.Rows != 2 || st.Skipped != 1 {
		t.Errorf("rows=%d skipped=%d, want 2/1", st.Rows, st.Skipped)
	}
	if st.From != 1768753965 || st.To != 1776243967 {
		t.Errorf("bounds %d..%d", st.From, st.To)
	}
	if st.Unsorted != 0 {
		t.Errorf("unsorted=%d", st.Unsorted)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\r\n"), "\n")
	if got := strings.TrimRight(lines[0], "\r"); got != csvHeader {
		t.Errorf("header = %q, want %q", got, csvHeader)
	}
	// amount0 scaled by 1e18, amount1 by 1e6, everything else verbatim.
	want1 := "0.0001,-0.332996,1768753965,2639910152900894115,4573075199760799433668080,-195208,3331.635787384446"
	if got := strings.TrimRight(lines[1], "\r"); got != want1 {
		t.Errorf("row1 =\n %q\nwant\n %q", got, want1)
	}
	want2 := "-0.404633371653689,1000,1776243967,1175758378896130813,3821062820594606087570255,-198802,2325.9972029162896"
	if got := strings.TrimRight(lines[2], "\r"); got != want2 {
		t.Errorf("row2 =\n %q\nwant\n %q", got, want2)
	}

	// The output must load back through the store's own reader.
	rows, err := parseCSV(strings.NewReader(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("reloaded %d rows, want 2", len(rows))
	}
	if rows[0].Amount0 != 0.0001 || rows[0].Amount1 != -0.332996 {
		t.Errorf("row0 amounts = %v / %v", rows[0].Amount0, rows[0].Amount1)
	}
	if rows[0].Tick != -195208 || rows[0].Price != 3331.635787384446 {
		t.Errorf("row0 tick/price = %v / %v", rows[0].Tick, rows[0].Price)
	}
	if rows[1].Amount1 != 1000 {
		t.Errorf("row1 amount1 = %v, want 1000", rows[1].Amount1)
	}
}
