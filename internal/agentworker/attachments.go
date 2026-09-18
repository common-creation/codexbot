package agentworker

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/common-creation/codexbot/internal/appserver"
	"github.com/common-creation/codexbot/internal/domain"
)

// Keep accepted uploads in the persistent agent home for subsequent turns.
func materializeAttachments(home, prompt string, attachments []domain.Attachment) (inputs []appserver.UserInput, dir string, err error) {
	if err = domain.ValidateAttachments(attachments); err != nil {
		return nil, "", err
	}
	if len(attachments) == 0 {
		return []appserver.UserInput{appserver.TextInput(prompt)}, "", nil
	}
	dir, err = os.MkdirTemp(home, ".codexbot-attachments-")
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	paths := make([]string, 0, len(attachments))
	images := make([]appserver.UserInput, 0, len(attachments))
	for i, attachment := range attachments {
		var data []byte
		data, err = base64.StdEncoding.Strict().DecodeString(attachment.Data)
		if err != nil {
			return nil, dir, err
		}
		fileDir := filepath.Join(dir, fmt.Sprint(i+1))
		if err = os.Mkdir(fileDir, 0700); err != nil {
			return nil, dir, err
		}
		path := filepath.Join(fileDir, filepath.Base(attachment.Name))
		if err = os.WriteFile(path, data, 0600); err != nil {
			return nil, dir, err
		}
		paths = append(paths, fmt.Sprintf("- %q: %q", attachment.Name, path))
		switch http.DetectContentType(data) {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
			images = append(images, appserver.UserInput{Type: appserver.InputLocalImage, Path: path})
		}
	}
	text := strings.TrimSpace(prompt + "\n\nAttached files (available at these local paths):\n" + strings.Join(paths, "\n"))
	inputs = append([]appserver.UserInput{appserver.TextInput(text)}, images...)
	return inputs, dir, nil
}
