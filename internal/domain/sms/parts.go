package sms

import (
	"strings"
	"unicode/utf16"
)

// The two alphabets a text message is sent in.
const (
	// GSM7 is the default 7-bit alphabet: 160 characters in one message.
	GSM7 = "GSM-7"
	// UCS2 is what a single character outside it (Persian, an emoji) switches
	// the whole message to: 70 characters in one message.
	UCS2 = "UCS-2"
)

const (
	gsmSingle, gsmPart = 160, 153
	ucsSingle, ucsPart = 70, 67
)

// gsmBasic is the GSM 03.38 default alphabet; gsmExtended are the characters
// that take two septets (an escape and the character).
const (
	gsmBasic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ ÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
		"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	gsmExtended = "\f^{}\\[~]|€"
)

// Size is what a text costs to send.
type Size struct {
	// Encoding is GSM7 or UCS2.
	Encoding string `json:"encoding"`
	// Units is the length in the encoding's units: septets or UTF-16 code units.
	Units int `json:"units"`
	// Parts is how many SMS the network splits the text into, each billed.
	Parts int `json:"parts"`
}

// Measure reports the encoding text needs and how many parts it is sent in.
func Measure(text string) Size {
	septets, gsm := 0, true

	for _, r := range text {
		switch {
		case strings.ContainsRune(gsmBasic, r):
			septets++
		case strings.ContainsRune(gsmExtended, r):
			septets += 2
		default:
			gsm = false
		}
	}

	if gsm {
		return Size{Encoding: GSM7, Units: septets, Parts: parts(septets, gsmSingle, gsmPart)}
	}

	units := len(utf16.Encode([]rune(text)))

	return Size{Encoding: UCS2, Units: units, Parts: parts(units, ucsSingle, ucsPart)}
}

func parts(units, single, part int) int {
	if units <= single {
		return 1
	}

	return (units + part - 1) / part
}
