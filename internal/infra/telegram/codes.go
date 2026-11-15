package telegram

import (
	"html"
	"strings"
	"unicode"
)

// A verification code is a run of this many digits standing on its own.
const minCode, maxCode = 4, 8

// joiners are what ties digits into something longer than a code: an amount
// (1,250,000), a date (2026/11/15), a time (12:30), a phone number (021-1234).
const joiners = ".,/:-"

// markCodes escapes content for Telegram HTML and wraps every verification
// code in <code>, which Telegram copies on a tap. A wrong guess costs
// nothing: the digits read the same, they are only easier to copy.
func markCodes(content string) string {
	runes := []rune(content)

	var out strings.Builder

	for i := 0; i < len(runes); {
		if !unicode.IsDigit(runes[i]) {
			out.WriteString(html.EscapeString(string(runes[i])))

			i++

			continue
		}

		end := i
		for end < len(runes) && unicode.IsDigit(runes[end]) {
			end++
		}

		run := string(runes[i:end])

		if n := end - i; n >= minCode && n <= maxCode && standsAlone(runes, i, end) {
			out.WriteString("<code>" + run + "</code>")
		} else {
			out.WriteString(run)
		}

		i = end
	}

	return out.String()
}

// standsAlone reports whether the digits at runes[start:end] are not part of
// a word or of a longer number.
func standsAlone(runes []rune, start, end int) bool {
	if start > 0 {
		prev := runes[start-1]
		if unicode.IsLetter(prev) {
			return false
		}

		// "code:1234" is a code; "6037-9912" is one number.
		if strings.ContainsRune(joiners, prev) && start > 1 && unicode.IsDigit(runes[start-2]) {
			return false
		}
	}

	if end == len(runes) {
		return true
	}

	next := runes[end]
	if unicode.IsLetter(next) {
		return false
	}

	return !strings.ContainsRune(joiners, next) || end+1 == len(runes) || !unicode.IsDigit(runes[end+1])
}
