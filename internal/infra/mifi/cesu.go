package mifi

import (
	"unicode/utf16"
	"unicode/utf8"
)

const (
	// surrogateLen is the size of one UTF-8-encoded UTF-16 surrogate half,
	// and pairLen of the two that make one character.
	surrogateLen = 3
	pairLen      = 2 * surrogateLen
	// highBase and lowBase are the second byte of a high and a low half with
	// its low nibble cleared: ED A0-AF xx and ED B0-BF xx.
	highBase = 0xA0
	lowBase  = 0xB0
	// payload masks the six value bits of a UTF-8 byte.
	payload = 0x3F
)

// fromCESU8 rewrites CESU-8 into UTF-8. The firmware encodes a character
// outside the BMP, which is every emoji, as its two UTF-16 surrogate halves of
// three bytes each; Go decodes those as invalid UTF-8, one U+FFFD per byte.
// Anything else passes through unchanged.
func fromCESU8(b []byte) []byte {
	var out []byte

	for i := 0; i < len(b); {
		if hi, ok := surrogate(b[i:], highBase); ok {
			if lo, ok := surrogate(b[i+surrogateLen:], lowBase); ok {
				if out == nil {
					out = append(make([]byte, 0, len(b)), b[:i]...)
				}

				out = utf8.AppendRune(out, utf16.DecodeRune(hi, lo))
				i += pairLen

				continue
			}
		}

		if out != nil {
			out = append(out, b[i])
		}

		i++
	}

	if out == nil {
		return b
	}

	return out
}

// surrogate decodes one 3-byte surrogate half at the start of b: ED A0-AF xx
// for a high half (highBase) or ED B0-BF xx for a low one (lowBase).
func surrogate(b []byte, base byte) (rune, bool) {
	if len(b) < surrogateLen || b[0] != 0xED || b[1]&0xF0 != base || b[2]&0xC0 != 0x80 {
		return 0, false
	}

	return 0xD000 | rune(b[1]&payload)<<6 | rune(b[2]&payload), true
}
