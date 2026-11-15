package telegram //nolint:testpackage // markCodes is internal.

import "testing"

func TestMarkCodes(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]string{
		"Your code is 7719.":          "Your code is <code>7719</code>.",
		"کد تایید: ۴۸۲۱۳":             "کد تایید: <code>۴۸۲۱۳</code>",
		"code:123456":                 "code:<code>123456</code>",
		"G-482913 is your code":       "G-<code>482913</code> is your code",
		"برداشت 1,250,000 ریال":       "برداشت 1,250,000 ریال",
		"1405/08/24 12:30":            "1405/08/24 12:30",
		"call 02112345678":            "call 02112345678",
		"card 6037-9912":              "card 6037-9912",
		"a<b & 12 > 3":                "a&lt;b &amp; 12 &gt; 3",
		"order ab1234 and 1234ab":     "order ab1234 and 1234ab",
		"use 9912, then press <done>": "use <code>9912</code>, then press &lt;done&gt;",
	} {
		if got := markCodes(text); got != want {
			t.Errorf("markCodes(%q)\n got %q\nwant %q", text, got, want)
		}
	}
}
