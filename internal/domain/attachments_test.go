package domain

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateAttachments(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []Attachment
		valid bool
	}{
		{"text", []Attachment{{"日本語.txt", "aGVsbG8="}}, true},
		{"empty file", []Attachment{{"empty.txt", ""}}, true},
		{"traversal", []Attachment{{"../secret", ""}}, false},
		{"windows path", []Attachment{{"a\\b", ""}}, false},
		{"control", []Attachment{{"a\nb", ""}}, false},
		{"malformed", []Attachment{{"a", "!!!!"}}, false},
		{"too many", make([]Attachment, 9), false},
		{"total too large", []Attachment{{"a", base64.StdEncoding.EncodeToString(make([]byte, 6<<20))}, {"b", base64.StdEncoding.EncodeToString(make([]byte, 5<<20))}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateAttachments(tc.files); (err == nil) != tc.valid {
				t.Fatalf("err=%v valid=%v", err, tc.valid)
			}
		})
	}
}
func TestDecodeMessageRejectsOversizeAndTrailingData(t *testing.T) {
	for _, input := range []string{`{} {}`, `{}` + strings.Repeat(" ", MaxMessageRequestBytes)} {
		var dst any
		if err := DecodeMessage(strings.NewReader(input), &dst); err == nil {
			t.Fatal("accepted invalid request")
		}
	}
}
