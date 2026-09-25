package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"

	"github.com/common-creation/codexbot/internal/store"
)

const (
	maxAgentIconBytes     = 2 << 20
	maxAgentIconDimension = 4096
	agentIconSize         = 512
	maxAgentSettingsBytes = 3 << 20 // Includes base64 expansion and other settings.
)

func decodeAgentSettings(w http.ResponseWriter, r *http.Request, value any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgentSettingsBytes))
	err := decoder.Decode(value)
	if err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "agent settings request is too large")
	} else {
		writeError(w, http.StatusBadRequest, "invalid JSON")
	}
	return false
}

func decodeAgentIcon(raw json.RawMessage) (*store.AgentIconUpdate, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return &store.AgentIconUpdate{}, nil
	}
	var upload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &upload); err != nil || upload.Data == "" {
		return nil, errors.New("icon must contain base64 image data or be null")
	}
	if len(upload.Data) > base64.StdEncoding.EncodedLen(maxAgentIconBytes) {
		return nil, errors.New("icon must be 2 MiB or smaller")
	}
	data, err := base64.StdEncoding.DecodeString(upload.Data)
	if err != nil {
		return nil, errors.New("icon data must be valid base64")
	}
	if len(data) > maxAgentIconBytes {
		return nil, errors.New("icon must be 2 MiB or smaller")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg" && format != "gif") {
		return nil, errors.New("icon must be a valid PNG, JPEG, or GIF image")
	}
	if config.Width < 1 || config.Height < 1 || config.Width > maxAgentIconDimension || config.Height > maxAgentIconDimension {
		return nil, errors.New("icon dimensions must be between 1 and 4096 pixels")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("icon image data is damaged")
	}
	// GIF frame bounds can differ from its logical screen dimensions, including
	// empty frames that the standard decoder accepts.
	bounds := source.Bounds()
	if bounds.Dx() < 1 || bounds.Dy() < 1 || bounds.Dx() > maxAgentIconDimension || bounds.Dy() > maxAgentIconDimension {
		return nil, errors.New("icon image dimensions must be between 1 and 4096 pixels")
	}
	var normalized bytes.Buffer
	if err := png.Encode(&normalized, resizeAgentIcon(source)); err != nil {
		return nil, errors.New("could not process icon image")
	}
	return &store.AgentIconUpdate{Data: normalized.Bytes()}, nil
}

// Average source pixels to keep small icons smooth without extra dependencies.
// Re-encoding also removes metadata and any trailing or animated content.
func resizeAgentIcon(source image.Image) image.Image {
	bounds := source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= agentIconSize && h <= agentIconSize {
		return source
	}
	dw, dh := agentIconSize, agentIconSize
	if w >= h {
		dh = max(1, h*agentIconSize/w)
	} else {
		dw = max(1, w*agentIconSize/h)
	}
	target := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, b, a, count uint64
			for sy := y * h / dh; sy < (y+1)*h/dh; sy++ {
				for sx := x * w / dw; sx < (x+1)*w/dw; sx++ {
					pr, pg, pb, pa := source.At(bounds.Min.X+sx, bounds.Min.Y+sy).RGBA()
					r, g, b, a = r+uint64(pr), g+uint64(pg), b+uint64(pb), a+uint64(pa)
					count++
				}
			}
			target.SetRGBA(x, y, color.RGBA{R: uint8(r / count >> 8), G: uint8(g / count >> 8), B: uint8(b / count >> 8), A: uint8(a / count >> 8)})
		}
	}
	return target
}

func (s *Server) getAgentIcon(w http.ResponseWriter, r *http.Request) {
	data, _, err := s.store.AgentIcon(r.Context(), r.PathValue("agentID"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "agent icon not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load agent icon")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Require authentication on each load, including after icon removal/logout.
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(data)
}
