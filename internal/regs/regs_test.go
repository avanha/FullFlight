package regs

import "testing"

type pair struct{ tail, hex string }

// Registrations and addresses seen on the air (adsb.lol, 31 Oct 2026), and from our own aircraft.
var usSeen = []pair{
	{"N575UP", "A762CC"}, {"N501DN", "A63DFF"}, {"N515DN", "A6745A"}, {"N24993", "A2551D"}, {"N393DA", "A48E3A"}, {"N2347X", "A21ABF"},
	{"N103HQ", "A01097"},
	{"N118DY", "A04A4D"},
	{"N12010", "A05646"},
	{"N120HQ", "A05470"},
	{"N135EV", "A08E3C"},
	{"N16147", "A0F869"},
	{"N17126", "A11FA1"},
	{"N184GJ", "A15027"},
	{"N199NV", "A18ADA"},
	{"N24542", "A24591"},
	{"N25MV", "A2564C"},
	{"N303NY", "A32BBA"},
	{"N305DN", "A3323D"},
	{"N333RA", "A3A26C"},
	{"N391BR", "A486A9"},
	{"N406AN", "A4C2F8"},
	{"N453FX", "A57DD4"},
	{"N481WN", "A5EE51"},
	{"N482LF", "A5F107"},
	{"N516DA", "A67805"},
	{"N527HE", "A6A3A3"},
	{"N529SC", "A6ABF0"},
	{"N572RJ", "A75757"},
	{"N623JB", "A8222C"},
	{"N640QS", "A866AA"},
	{"N658DL", "A8AAC8"},
	{"N7168L", "A995D2"},
	{"N753US", "AA2701"},
	{"N768YX", "AA617C"},
	{"N821BE", "AB34B0"},
	{"N836AW", "AB6EB9"},
	{"N837SK", "AB73F5"},
	{"N862DN", "ABD69D"},
	{"N885NN", "AC31A1"},
	{"N920SB", "ACBFBC"},
	{"N928XJ", "ACDDF8"},
	{"N93A", "ACE350"},
	{"N971MC", "AD8872"},
	{"N988AL", "ADC8E7"},
	{"N989NN", "ADCDCC"},
}

var caSeen = []pair{
	{"C-FNOH", "C023C8"}, // Air Canada 25, from the log
	{"C-FCLS", "C00679"},
	{"C-FCQD", "C006EC"},
	{"C-FCSX", "C00734"},
	{"C-FFOP", "C00EB0"},
	{"C-FGEO", "C0104F"},
	{"C-FHMO", "C013C3"},
	{"C-FLKA", "C01E11"},
	{"C-FNOI", "C023C9"},
	{"C-FXWJ", "C03F02"},
	{"C-GAZI", "C0473B"},
	{"C-GBFJ", "C047D8"},
	{"C-GBFY", "C047E7"},
	{"C-GEPQ", "C050CF"},
	{"C-GFCH", "C05218"},
	{"C-GGBL", "C054A6"},
	{"C-GHQG", "C058CB"},
	{"C-GJXE", "C05EC7"},
	{"C-GJZV", "C05F0C"},
	{"C-GJZY", "C05F0F"},
	{"C-GKQC", "C060B3"},
	{"C-GKYN", "C0618E"},
	{"C-GLLZ", "C062EC"},
	{"C-GLQN", "C06362"},
	{"C-GLQX", "C0636C"},
	{"C-GOFW", "C06A39"},
	{"C-GPZV", "C06EE4"},
	{"C-GRSP", "C07370"},
	{"C-GTGH", "C07778"},
	{"C-GTOT", "C07854"},
	{"C-GWUS", "C080DB"},
}

var auSeen = []pair{
	{"VH-8IB", "7CAD41"},
	{"VH-A5A", "7C045C"},
	{"VH-IJW", "7C29DA"},
	{"VH-IRN", "7C2AF1"},
	{"VH-LQB", "7C39F1"},
	{"VH-LQM", "7C39FC"},
	{"VH-NMG", "7C4386"},
	{"VH-QPI", "7C5324"},
	{"VH-RIB", "7C5731"},
	{"VH-SKR", "7C5C99"},
	{"VH-UAH", "7C6547"},
	{"VH-VFC", "7C6B06"},
	{"VH-VFQ", "7C6B14"},
	{"VH-VGT", "7C6B3B"},
	{"VH-VKI", "7C6BC0"},
	{"VH-VMS", "7C6C12"},
	{"VH-VVY", "7C6D5C"},
	{"VH-VXB", "7C6D8D"},
	{"VH-VZM", "7C6DE0"},
	{"VH-X3E", "7C7888"},
	{"VH-X4F", "7C78AD"},
	{"VH-X4I", "7C78B0"},
	{"VH-XZC", "7C77F6"},
	{"VH-XZJ", "7C77FD"},
	{"VH-YIZ", "7C7AB9"},
	{"VH-YQG", "7C7BC6"},
	{"VH-YQR", "7C7BD1"},
	{"VH-ZIO", "7C7FBE"},
	{"VH-ZRC", "7C80F6"},
	{"VH-ZXQ", "7C81DC"},
}

func TestSeenPairs(t *testing.T) {
	for _, p := range append(append(append([]pair{}, usSeen...), caSeen...), auSeen...) {
		if got, ok := HexForTail(p.tail); !ok || got != p.hex {
			t.Errorf("HexForTail(%s) = %s %v, want %s", p.tail, got, ok, p.hex)
		}
		if got, ok := TailForHex(p.hex); !ok || got != p.tail && got != "C-"+p.tail[1:] {
			t.Errorf("TailForHex(%s) = %s %v, want %s", p.hex, got, ok, p.tail)
		}
	}
}

func TestEnds(t *testing.T) {
	for _, p := range []pair{{"N1", "A00001"}, {"N1A", "A00002"}, {"N1AA", "A00003"}, {"N1AB", "A00004"}, {"N1B", "A0001B"}, {"N10", "A0025A"},
		{"N99999", "ADF7C7"}, {"C-FAAA", "C00001"}, {"C-FZZZ", "C044A8"}, {"C-GAAA", "C044A9"}, {"C-GZZZ", "C08950"},
		{"VH-AAA", "7C0000"}, {"VH-AAB", "7C0001"}, {"VH-AAZ", "7C0019"}, {"VH-AA0", "7C001A"}, {"VH-ABA", "7C0024"}, {"VH-999", "7CB63F"}} {
		if got, ok := HexForTail(p.tail); !ok || got != p.hex {
			t.Errorf("HexForTail(%s) = %s %v, want %s", p.tail, got, ok, p.hex)
		}
	}
}

// Every address in both ranges names a registration that names the address back, and no two addresses share one.
func TestRoundTrip(t *testing.T) {
	seen := map[string]bool{}
	check := func(lo, n int) {
		for a := lo; a < lo+n; a++ {
			h := hex6(a)
			tail, ok := TailForHex(h)
			if !ok {
				t.Fatalf("%s: no registration", h)
			}
			if seen[tail] {
				t.Fatalf("%s is the registration of two addresses", tail)
			}
			seen[tail] = true
			if back, ok := HexForTail(tail); !ok || back != hexUpper(h) {
				t.Fatalf("%s -> %s -> %s", h, tail, back)
			}
		}
	}
	check(usStart, usCount)
	check(caFStart, caCount)
	check(caGStart, caCount)
	check(auStart, auCount)
}

func hexUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestNotWorkedOut(t *testing.T) {
	for _, tail := range []string{"", "N", "N0", "N01", "N1I", "N1O", "N1AI", "N123456", "N12345A", "N1234AB", "N1ABC", "NAB", "N12A3",
		"B-LRM", "9V-SGC", "VH-OQ", "VH-OQAB", "VH-O-A", "VH-oq!", "VX-OQA", "C-IABC", "C-FAB", "C-FABCD", "C-F1BC", "G-ABCD"} {
		if h, ok := HexForTail(tail); ok {
			t.Errorf("HexForTail(%q) = %s, want none", tail, h)
		}
	}
	for _, h := range []string{"", "780AB8", "ADF7C8", "A00000", "C08951", "7CB640", "7BFFFF", "C044A", "XYZ123", "71BE26"} {
		if tail, ok := TailForHex(h); ok {
			t.Errorf("TailForHex(%q) = %s, want none", h, tail)
		}
	}
	// lower case and a missing dash are fine
	if h, ok := HexForTail("c-fivq"); !ok || h == "" {
		t.Errorf("c-fivq: %s %v", h, ok)
	}
	if h, _ := HexForTail("n575up"); h != "A762CC" {
		t.Errorf("n575up = %s", h)
	}
}
