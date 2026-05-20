package mitmproxy

import (
	"strings"
	"testing"
)

func TestCaptureScriptMasksSensitiveHeaders(t *testing.T) {
	files := RuntimeFiles(Config{})
	script := files["mitm_capture.py"]
	if !strings.Contains(script, "MASKED_HEADERS") {
		t.Fatal("expected capture script to define masked headers")
	}
	if !strings.Contains(script, "def mask_secret") {
		t.Fatal("expected capture script to mask sensitive values")
	}
	if strings.Contains(script, `"<redacted>"`) {
		t.Fatal("capture script should mask values instead of replacing them with <redacted>")
	}
	if !strings.Contains(script, "UNMASK_TOKEN") {
		t.Fatal("expected capture script to support unmasking")
	}
}

func TestEnvironmentCanUnmaskToken(t *testing.T) {
	env := Environment(Config{UnmaskToken: true})
	if !containsString(env, "INSPECT_UNMASK_TOKEN=1") {
		t.Fatalf("expected unmask env, got %#v", env)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
