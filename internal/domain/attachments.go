package domain

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxAttachments = 8
const MaxAttachmentBytes = 10 << 20
const MaxMessageRequestBytes = 15 << 20

type Attachment struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

// DecodeMessage bounds the encoded upload, including trailing data.
func DecodeMessage(body io.Reader, dst any) error {
	data, err := io.ReadAll(io.LimitReader(body, MaxMessageRequestBytes+1))
	if err != nil {
		return err
	}
	if len(data) > MaxMessageRequestBytes {
		return fmt.Errorf("message exceeds 15 MiB")
	}
	return json.Unmarshal(data, dst)
}

func ValidateAttachments(attachments []Attachment) error {
	if len(attachments) > MaxAttachments {
		return fmt.Errorf("at most 8 attachments are allowed")
	}
	total := 0
	for _, attachment := range attachments {
		name := attachment.Name
		if strings.TrimSpace(name) == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
			return fmt.Errorf("invalid attachment filename")
		}
		if len(attachment.Data) > base64.StdEncoding.EncodedLen(MaxAttachmentBytes) {
			return fmt.Errorf("attachments exceed 10 MiB")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(attachment.Data)
		if err != nil {
			return fmt.Errorf("invalid attachment base64 data")
		}
		total += len(data)
		if total > MaxAttachmentBytes {
			return fmt.Errorf("attachments exceed 10 MiB")
		}
	}
	return nil
}

func AttachmentPrompt(prompt string, attachments []Attachment) string {
	if len(attachments) == 0 {
		return prompt
	}
	lines := make([]string, 0, len(attachments)+1)
	if text := strings.TrimSpace(prompt); text != "" {
		lines = append(lines, text)
	}
	for _, attachment := range attachments {
		lines = append(lines, "Attached file: "+attachment.Name)
	}
	return strings.Join(lines, "\n")
}
