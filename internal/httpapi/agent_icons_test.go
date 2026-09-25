package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/auth"
	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/runtimeclient"
)

func iconTestPNG(t *testing.T, width, height int, fill color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func iconTestJSON(t *testing.T, data []byte) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDecodeAgentIconValidatesAndNormalizes(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2, 1))
	source.Set(0, 0, color.RGBA{R: 255, A: 255})
	for _, format := range []string{"png", "jpeg", "gif"} {
		t.Run(format, func(t *testing.T) {
			var input bytes.Buffer
			var err error
			switch format {
			case "png":
				err = png.Encode(&input, source)
			case "jpeg":
				err = jpeg.Encode(&input, source, nil)
			case "gif":
				err = gif.Encode(&input, source, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			input.WriteString("untrusted trailing content")
			icon, err := decodeAgentIcon(iconTestJSON(t, input.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			config, outputFormat, err := image.DecodeConfig(bytes.NewReader(icon.Data))
			if err != nil || outputFormat != "png" || config.Width != 2 || config.Height != 1 || bytes.Contains(icon.Data, []byte("untrusted")) {
				t.Fatalf("normalized=%+v format=%s err=%v", config, outputFormat, err)
			}
		})
	}
	large := iconTestPNG(t, 1024, 512, color.NRGBA{R: 255, A: 128})
	icon, err := decodeAgentIcon(iconTestJSON(t, large))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(icon.Data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 512 || img.Bounds().Dy() != 256 {
		t.Fatalf("thumbnail bounds=%v", img.Bounds())
	}
	_, _, _, alpha := img.At(0, 0).RGBA()
	if alpha != 128*257 {
		t.Fatalf("thumbnail lost alpha: %d", alpha)
	}
	valid := iconTestPNG(t, 1, 1, color.White)
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"empty", json.RawMessage(`{}`)},
		{"wrong type", json.RawMessage(`"icon"`)},
		{"invalid base64", json.RawMessage(`{"data":"%%%"}`)},
		{"unsupported SVG", iconTestJSON(t, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))},
		{"too many bytes", iconTestJSON(t, make([]byte, maxAgentIconBytes+1))},
		{"too wide", iconTestJSON(t, iconTestPNG(t, maxAgentIconDimension+1, 1, color.White))},
		{"damaged image", iconTestJSON(t, valid[:len(valid)-16])},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeAgentIcon(tc.raw); err == nil {
				t.Fatal("invalid icon accepted")
			}
		})
	}
}

func TestAgentIconRejectsDecodedEmptyGIFFrame(t *testing.T) {
	// A nonempty logical screen containing a zero-width image frame is accepted
	// by image/gif. Resizing that frame previously divided by zero.
	data := []byte{'G', 'I', 'F', '8', '9', 'a', 1, 0, 0, 4, 0x80, 0, 0, 0, 0, 0, 255, 255, 255, 0x2c, 0, 0, 0, 0, 0, 0, 0, 4, 0, 2, 1, 0x2c, 0, 0x3b}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || source.Bounds().Dx() != 0 || source.Bounds().Dy() != 1024 {
		t.Fatalf("fixture must decode to an empty frame: image=%v err=%v", source, err)
	}
	_, err = decodeAgentIcon(iconTestJSON(t, data))
	if err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("decoded frame must fail dimension validation: %v", err)
	}
}

func TestAgentIconAuthenticatedLifecycle(t *testing.T) {
	st := permissionTestStore(t)
	ctx := context.Background()
	if err := st.CreateAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, auth.TokenHash("session"), "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Icon persistence must also work when starting the agent runtime fails.
	handler := New(st, runtimeclient.New("http://127.0.0.1:1", "token"), Config{}, nil).Handler()
	request := func(method, path string, body any, authenticated, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, &buf)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "session"})
		}
		if csrf {
			req.Header.Set("X-CSRF-Token", "csrf")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	input := iconTestPNG(t, 2, 2, color.White)
	// A payload over the old 1 MiB JSON limit must be accepted and normalized.
	input = append(input, make([]byte, 1<<20)...)
	settings := map[string]any{"name": "Agent", "rolePrompt": "Role", "icon": iconTestJSON(t, input)}
	response := request("POST", "/api/agents", settings, true, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var a domain.Agent
	if err := json.Unmarshal(response.Body.Bytes(), &a); err != nil || a.IconURL == "" {
		t.Fatalf("created=%+v err=%v", a, err)
	}
	url := a.IconURL
	if response = request("GET", url, nil, false, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous icon status=%d", response.Code)
	}
	response = request("GET", url, nil, true, false)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("icon status=%d headers=%v", response.Code, response.Header())
	}
	if _, err := png.Decode(response.Body); err != nil {
		t.Fatal(err)
	}
	response = request("GET", "/api/agents", nil, true, false)
	var list []domain.Agent
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].IconURL != url {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	path := "/api/agents/" + a.ID
	if response = request("PATCH", path, settings, true, false); response.Code != http.StatusForbidden {
		t.Fatalf("no CSRF status=%d", response.Code)
	}
	c := domain.Conversation{ID: "conversation", AgentID: a.ID, Kind: "manual", RoleVersion: 1, CodexThreadID: "preserved-thread", CreatedAt: time.Now()}
	if err := st.CreateConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, domain.Run{ID: "run", AgentID: a.ID, ConversationID: c.ID, Source: "manual", Prompt: "hello", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	settings["icon"] = iconTestJSON(t, iconTestPNG(t, 2, 2, color.Black))
	if response = request("PATCH", path, settings, true, true); response.Code != http.StatusOK {
		t.Fatalf("update during run status=%d body=%s", response.Code, response.Body.String())
	}
	stored, err := st.Agent(ctx, a.ID)
	if err != nil || stored.RoleVersion != a.RoleVersion || stored.IconURL == url || stored.IconURL == "" {
		t.Fatalf("updated=%+v err=%v", stored, err)
	}
	url = stored.IconURL
	conversation, err := st.Conversation(ctx, c.ID)
	if err != nil || conversation.CodexThreadID != c.CodexThreadID {
		t.Fatalf("conversation changed: %+v err=%v", conversation, err)
	}
	delete(settings, "icon")
	if response = request("PATCH", path, settings, true, true); response.Code != http.StatusOK {
		t.Fatalf("preserve status=%d", response.Code)
	}
	stored, _ = st.Agent(ctx, a.ID)
	if stored.IconURL != url {
		t.Fatal("omission removed the icon")
	}
	settings["icon"] = iconTestJSON(t, []byte("invalid image"))
	settings["name"] = "Should not save"
	if response = request("PATCH", path, settings, true, true); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d", response.Code)
	}
	stored, _ = st.Agent(ctx, a.ID)
	if stored.Name != a.Name || stored.IconURL != url {
		t.Fatalf("invalid upload partially saved: %+v", stored)
	}
	settings["name"] = a.Name
	settings["icon"] = nil
	if response = request("PATCH", path, settings, true, true); response.Code != http.StatusOK {
		t.Fatalf("remove status=%d", response.Code)
	}
	stored, _ = st.Agent(ctx, a.ID)
	if stored.IconURL != "" || stored.RoleVersion != a.RoleVersion {
		t.Fatalf("removed=%+v", stored)
	}
	if response = request("GET", url, nil, true, false); response.Code != http.StatusNotFound {
		t.Fatalf("removed image served: status=%d", response.Code)
	}
}

func TestAgentSettingsRequestLimit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"oversized", `{"icon":{"data":"` + strings.Repeat("A", maxAgentSettingsBytes) + `"}}`, http.StatusRequestEntityTooLarge},
		{"trailing JSON", `{"name":"Agent"} {}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/agents", strings.NewReader(tc.body))
			response := httptest.NewRecorder()
			var value any
			if decodeAgentSettings(response, req, &value) || response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
