package networking

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestECHGatewayRealWARP registers a real WARP account through the gateway, like the app's
// ECH route (CloudflareClient's third route) does when the direct route is blocked. Each case
// uses a fresh key, as every registration must. Skipped without BPB_E2E=1: it needs internet.
func TestECHGatewayRealWARP(t *testing.T) {
	if os.Getenv("BPB_E2E") == "" {
		t.Skip("BPB_E2E not set")
	}
	if err := Start("127.0.0.1:10811"); err != nil {
		t.Fatal(err)
	}
	defer Stop()
	time.Sleep(200 * time.Millisecond)

	for _, tc := range []struct {
		name   string
		dialIP func(t *testing.T) string
	}{
		{"pinned-188.114.96.0", func(*testing.T) string { return "188.114.96.0:443" }},
		{"pinned-resolved", func(t *testing.T) string { return firstIPv4(t, "api.cloudflareclient.com") + ":443" }},
		{"unpinned", func(*testing.T) string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := registrationBody(t)
			req, _ := http.NewRequest("POST", "http://127.0.0.1:10811/v0a4005/reg", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "insomnia/13.0.2")
			req.Header.Set("X-SNI", "api.cloudflareclient.com")
			if dial := tc.dialIP(t); dial != "" {
				req.Header.Set("X-Dial", dial)
			}
			client := &http.Client{Timeout: 30 * time.Second}
			res, err := client.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer res.Body.Close()
			raw, _ := io.ReadAll(res.Body)
			if res.Header.Get(ErrorHeader) != "" {
				t.Fatalf("gateway error: %s", string(raw[:min(200, len(raw))]))
			}
			if res.StatusCode != 200 {
				t.Fatalf("status %d: %s", res.StatusCode, string(raw[:min(200, len(raw))]))
			}
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("bad json: %v", err)
			}
			if out["config"] == nil {
				t.Fatalf("no config in response")
			}
		})
	}
}

// registrationBody builds one fresh registration request, like lib/services/warp.dart.
func registrationBody(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"install_id": "",
		"fcm_token":  "",
		"tos":        time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"type":       "Android",
		"model":      "PC",
		"locale":     "en_US",
		"warp_enabled": true,
		"key":        base64.StdEncoding.EncodeToString(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func firstIPv4(t *testing.T, host string) string {
	t.Helper()
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		t.Fatalf("lookup %s: %v", host, err)
	}
	for _, ip := range ips {
		if v := ip.String(); strings.Count(v, ":") < 2 {
			return v
		}
	}
	t.Fatalf("no IPv4 for %s", host)
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
