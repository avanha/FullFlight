// Package regs works out an aircraft's ICAO hex address from its registration, and the registration from the address, for
// the countries that allocate addresses by a rule: the United States (N-numbers), Canada (C-Fxxx, C-Gxxx) and Australia (VH-xxx). Everywhere
// else the address was handed out one aircraft at a time and has to be looked up.
//
// The United States gives its 915,399 N-numbers the addresses A00001 to ADF7C7 in order. The order is that of the number
// written as a tree: N1, then N1A and N1AA to N1ZZ (letters I and O are never used), then N10 and everything under it,
// N11, and so on to N19, then N2. Each number can be followed by one or two letters until it has four digits, which
// can take one, and a five digit number takes none.
//
// Canada gives C-FAAA to C-FZZZ the addresses C00001 to C044A8 and C-GAAA to C-GZZZ the next 17,576, in alphabetical order.
//
// Australia gives VH-AAA the address 7C0000 and counts up from there with each of the three characters a digit in base 36,
// the letters first and then 0 to 9, so VH-AAB is 7C0001, VH-AAZ is 7C0019, VH-AA0 is 7C001A and VH-ABA is 7C0024. (The
// address block is larger than the 46,656 registrations that fit; the rest are not named by a rule.)
package regs

import (
	"strings"
)

const (
	usStart  = 0xA00001
	usCount  = 915399
	usBucket = 101711 // the numbers that begin with one given digit, N1 for example

	caFStart = 0xC00001
	caGStart = 0xC044A9
	caCount  = 26 * 26 * 26

	auStart = 0x7C0000
	auCount = 36 * 36 * 36
)

// auChars are the characters of a VH- registration, in order.
const auChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// usLetters are the letters an N-number can use.
const usLetters = "ABCDEFGHJKLMNPQRSTUVWXYZ"

// For the digits after the first of an N-number: the room taken by the number itself and its letter suffixes before the
// numbers that extend it with another digit, and what each such digit adds. A number of one to three digits has itself
// and 24 letters, each with 24 two-letter suffixes after it (601); one of four digits has itself and 24 single letters (25).
var (
	usBlock = [4]int{601, 601, 601, 25}
	usStep  = [4]int{10111, 951, 35, 1}
)

// HexForTail returns the hex address for a registration, upper case, and whether the registration is one we can work out.
// The dash may be left out or kept (C-FIVQ or CFIVQ), and N-numbers need none.
func HexForTail(tail string) (string, bool) {
	t := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(tail), "-", ""))
	var n int
	var ok bool
	switch {
	case strings.HasPrefix(t, "N"):
		n, ok = usHex(t[1:])
	case strings.HasPrefix(t, "C") && len(t) == 5:
		n, ok = caHex(t)
	case strings.HasPrefix(t, "VH") && len(t) == 5:
		n, ok = auHex(t[2:])
	}
	if !ok {
		return "", false
	}
	return strings.ToUpper(hex6(n)), true
}

// TailForHex returns the registration for a hex address, and whether it lies in a range we can work out.
func TailForHex(hexAddr string) (string, bool) {
	n, ok := parseHex(hexAddr)
	if !ok {
		return "", false
	}
	switch {
	case n >= usStart && n < usStart+usCount:
		return usTail(n - usStart), true
	case n >= caFStart && n < caFStart+caCount:
		return "C-F" + caLetters(n-caFStart), true
	case n >= caGStart && n < caGStart+caCount:
		return "C-G" + caLetters(n-caGStart), true
	case n >= auStart && n < auStart+auCount:
		off := n - auStart
		return "VH-" + string([]byte{auChars[off/1296], auChars[off/36%36], auChars[off%36]}), true
	}
	return "", false
}

func hex6(n int) string {
	const digits = "0123456789abcdef"
	var b [6]byte
	for i := 5; i >= 0; i-- {
		b[i] = digits[n&15]
		n >>= 4
	}
	return string(b[:])
}

func parseHex(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 6 {
		return 0, false
	}
	n := 0
	for _, c := range strings.ToLower(s) {
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'f':
			v = int(c-'a') + 10
		default:
			return 0, false
		}
		n = n<<4 | v
	}
	return n, true
}

// usHex is the address of an N-number, given without the N ("575UP").
func usHex(rest string) (int, bool) {
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	digits, letters := rest[:i], rest[i:]
	if len(digits) == 0 || digits[0] == '0' || len(digits)+len(letters) > 5 {
		return 0, false
	}
	off := int(digits[0]-'1') * usBucket
	for k := 1; k < len(digits); k++ {
		off += usBlock[k-1] + int(digits[k]-'0')*usStep[k-1]
	}
	switch {
	case letters == "":
	case len(digits) == 5 || len(letters) > 2 || (len(digits) == 4 && len(letters) > 1):
		return 0, false
	default:
		a := strings.IndexByte(usLetters, letters[0])
		if a < 0 {
			return 0, false
		}
		if len(digits) == 4 {
			off += 1 + a // one letter, directly after the number
		} else {
			off += 1 + 25*a // each first letter is followed by its 24 second letters
		}
		if len(letters) == 2 {
			b := strings.IndexByte(usLetters, letters[1])
			if b < 0 {
				return 0, false
			}
			off += 1 + b
		}
	}
	return usStart + off, true
}

// usTail is the N-number at an offset from the first address.
func usTail(off int) string {
	out := []byte{'N', byte('1' + off/usBucket)}
	off %= usBucket
	for k := 0; ; k++ {
		if off == 0 {
			return string(out)
		}
		if off < usBlock[k] {
			j := off - 1
			if k == 3 {
				return string(append(out, usLetters[j]))
			}
			out = append(out, usLetters[j/25])
			if r := j % 25; r > 0 {
				out = append(out, usLetters[r-1])
			}
			return string(out)
		}
		off -= usBlock[k]
		out = append(out, byte('0'+off/usStep[k]))
		off %= usStep[k]
	}
}

// caHex is the address of a Canadian registration written without its dash ("CFIVQ").
func caHex(t string) (int, bool) {
	var start int
	switch t[1] {
	case 'F':
		start = caFStart
	case 'G':
		start = caGStart
	default:
		return 0, false
	}
	n := 0
	for _, c := range t[2:] {
		if c < 'A' || c > 'Z' {
			return 0, false
		}
		n = n*26 + int(c-'A')
	}
	return start + n, true
}

func caLetters(n int) string {
	return string([]byte{byte('A' + n/676), byte('A' + n/26%26), byte('A' + n%26)})
}

// auHex is the address of a VH- registration, given as its three characters ("OQA").
func auHex(rest string) (int, bool) {
	n := 0
	for i := 0; i < len(rest); i++ {
		d := strings.IndexByte(auChars, rest[i])
		if d < 0 {
			return 0, false
		}
		n = n*36 + d
	}
	return auStart + n, true
}
