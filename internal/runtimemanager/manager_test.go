package runtimemanager

import (
	"net/http/httptest"
	"testing"
)

func TestWorkerProxyPathMapsLiteralAuthStatus(t *testing.T) {
	request := httptest.NewRequest("GET", "http://runtime-manager/v1/agents/agent-id/auth/status", nil)
	path, ok := workerProxyPath(request)
	if !ok || path != "/v1/auth/status" {
		t.Fatalf("path=%q ok=%v", path, ok)
	}
}

func TestWorkerProxyPathMapsAuthAction(t *testing.T) {
	request := httptest.NewRequest("POST", "http://runtime-manager/v1/agents/agent-id/auth/device", nil)
	request.SetPathValue("action", "device")
	path, ok := workerProxyPath(request)
	if !ok || path != "/v1/auth/device" {
		t.Fatalf("path=%q ok=%v", path, ok)
	}
}
