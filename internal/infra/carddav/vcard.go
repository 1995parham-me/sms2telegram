package carddav

import (
	"strings"

	"github.com/1995parham-me/sms2telegram/internal/domain/contacts"
)

// Parse reads the display name and phone numbers of one vCard (3.0 or 4.0).
// The name is FN, falling back to ORG and then to N. Anything it does not
// understand is skipped, never an error: one odd card must not cost the rest.
func Parse(card string) contacts.Card {
	var fn, org, n string

	var phones []string

	for _, line := range unfold(card) {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		// Drop the parameters (TEL;TYPE=cell) and an Apple-style group (item1.TEL).
		name, _, _ = strings.Cut(name, ";")
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}

		switch strings.ToUpper(name) {
		case "FN":
			fn = unescape(value)
		case "ORG":
			org = strings.Trim(strings.ReplaceAll(unescape(value), ";", " "), " ")
		case "N":
			n = structuredName(value)
		case "TEL":
			if tel := strings.TrimSpace(strings.TrimPrefix(value, "tel:")); tel != "" {
				phones = append(phones, tel)
			}
		}
	}

	name := fn
	if strings.TrimSpace(name) == "" {
		name = org
	}

	if strings.TrimSpace(name) == "" {
		name = n
	}

	return contacts.Card{Name: strings.TrimSpace(name), Phones: phones}
}

// unfold joins continuation lines (RFC 6350 3.2) and splits on CRLF or LF.
func unfold(card string) []string {
	var lines []string

	for raw := range strings.SplitSeq(strings.ReplaceAll(card, "\r\n", "\n"), "\n") {
		if (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += raw[1:]

			continue
		}

		lines = append(lines, raw)
	}

	return lines
}

// structuredName turns N:Family;Given;Middle;Prefix;Suffix into "Given Family".
func structuredName(value string) string {
	parts := strings.Split(value, ";")

	var words []string

	for _, i := range []int{3, 1, 2, 0, 4} {
		if i < len(parts) {
			if w := strings.TrimSpace(unescape(parts[i])); w != "" {
				words = append(words, w)
			}
		}
	}

	return strings.Join(words, " ")
}

func unescape(value string) string {
	return strings.NewReplacer(`\n`, " ", `\N`, " ", `\,`, ",", `\;`, ";", `\\`, `\`).Replace(value)
}
