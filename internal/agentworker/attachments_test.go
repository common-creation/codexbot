package agentworker

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/common-creation/codexbot/internal/appserver"
	"github.com/common-creation/codexbot/internal/domain"
)

func TestMaterializeAttachmentsPreservesBytesAndDetectsImages(t *testing.T) {
	home := t.TempDir()
	png := "\x89PNG\r\n\x1a\nimage-data"
	attachments := []domain.Attachment{{Name: "same.txt", Data: base64.StdEncoding.EncodeToString([]byte("first"))}, {Name: "same.txt", Data: base64.StdEncoding.EncodeToString([]byte("second"))}, {Name: "image.bin", Data: base64.StdEncoding.EncodeToString([]byte(png))}, {Name: "fake.png", Data: base64.StdEncoding.EncodeToString([]byte("ordinary text"))}}
	inputs, dir, err := materializeAttachments(home, "", attachments)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != home || len(inputs) != 2 || inputs[0].Type != appserver.InputText || inputs[1].Type != appserver.InputLocalImage {
		t.Fatalf("dir=%s inputs=%+v", dir, inputs)
	}
	for i, want := range []string{"first", "second"} {
		data, err := os.ReadFile(filepath.Join(dir, string(rune('1'+i)), "same.txt"))
		if err != nil || string(data) != want {
			t.Fatalf("data=%q err=%v", data, err)
		}
	}
	data, err := os.ReadFile(inputs[1].Path)
	if err != nil || string(data) != png || !strings.Contains(inputs[0].Text, inputs[1].Path) {
		t.Fatalf("image data=%q err=%v inputs=%+v", data, err, inputs)
	}
	_, secondDir, err := materializeAttachments(home, "again", attachments[:1])
	if err != nil || secondDir == dir {
		t.Fatalf("second dir=%s err=%v", secondDir, err)
	}
}
func TestMaterializeAttachmentsRejectsUnsafeNameBeforeWriting(t *testing.T) {
	home := t.TempDir()
	if _, _, err := materializeAttachments(home, "", []domain.Attachment{{Name: "../escape"}}); err == nil {
		t.Fatal("accepted traversal")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}
