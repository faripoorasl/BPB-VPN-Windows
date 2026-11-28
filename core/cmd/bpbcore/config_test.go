package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startOptions for a proxy-mode start (no TUN).
func testOptions(config string) *startOptions {
	return &startOptions{
		Config:     config,
		Remark:     "Test",
		PerAppMode: "off",
		MTU:        1500,
		ProxyOnly:  true,
	}
}

const testBaseConfig = `{
  "log": {"loglevel": "warning"},
  "inbounds": [
    {"tag": "tun", "protocol": "tun", "settings": {"name": "xray0", "MTU": 1500}},
    {"tag": "mixed-in", "listen": "127.0.0.1", "port": 10808, "protocol": "mixed"}
  ],
  "outbounds": [
    {"protocol": "vless", "tag": "proxy-1"},
    {"protocol": "freedom", "tag": "direct"},
    {"protocol": "blackhole", "tag": "block"},
    {"protocol": "dns", "tag": "dns-out"}
  ],
  "routing": {"rules": [
    {"type": "field", "inboundTag": ["tun", "mixed-in"], "port": 53, "outboundTag": "dns-out"},
    {"type": "field", "ip": ["0.0.0.0/0", "::/0"], "outboundTag": "proxy-1"}
  ]}
}`

func decode(t *testing.T, config string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(config), &m); err != nil {
		t.Fatalf("adapted config is not JSON: %v", err)
	}
	return m
}

func rulesOf(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	routing := m["routing"].(map[string]any)
	list, _ := routing["rules"].([]any)
	var rules []map[string]any
	for _, r := range list {
		rules = append(rules, r.(map[string]any))
	}
	return rules
}

func inboundsOf(t *testing.T, m map[string]any) map[string]map[string]any {
	t.Helper()
	list, _ := m["inbounds"].([]any)
	out := map[string]map[string]any{}
	for _, i := range list {
		inbound := i.(map[string]any)
		tag, _ := inbound["tag"].(string)
		settings, _ := inbound["settings"].(map[string]any)
		out[tag] = settings
	}
	return out
}

// attachLogs points Xray's logs at the data dir.
func TestAttachLogs(t *testing.T) {
	dir := t.TempDir()
	config := attachLogs(`{"log": {"loglevel": "warning"}}`, dir)
	m := decode(t, config)
	log, _ := m["log"].(map[string]any)
	want := filepath.Join(dir, "xray.log")
	if log["error"] != want || log["access"] != want {
		t.Fatalf("logs not attached: %v", log)
	}
}

// adaptConfig adds the Windows TUN settings (VPN mode) and leaves proxy mode alone.
func TestAdaptConfigTun(t *testing.T) {
	dir := t.TempDir()

	// VPN mode: the TUN inbound gets the system routes, DNS and leak protection.
	adapted, errText := adaptConfig(testBaseConfig, &startOptions{
		Config: testBaseConfig, Remark: "Test", PerAppMode: "off", MTU: 1420,
	}, dir)
	if errText != "" {
		t.Fatalf("adaptConfig: %s", errText)
	}
	settings := inboundsOf(t, decode(t, adapted))["tun"]
	if settings == nil {
		t.Fatal("tun inbound lost")
	}
	if settings["MTU"] != float64(1420) {
		t.Fatalf("MTU not applied: %v", settings["MTU"])
	}
	gateway, _ := settings["gateway"].([]any)
	if len(gateway) != 2 || gateway[0] != tunIPv4 || gateway[1] != tunIPv6 {
		t.Fatalf("gateway wrong: %v", gateway)
	}
	dns, _ := settings["dns"].([]any)
	if len(dns) != 1 || dns[0] != tunDNS {
		t.Fatalf("dns wrong: %v", dns)
	}
	routes, _ := settings["autoSystemRoutingTable"].([]any)
	if len(routes) != 2 || routes[0] != "0.0.0.0/0" || routes[1] != "::/0" {
		t.Fatalf("routes wrong: %v", routes)
	}
	leaks, _ := settings["autoSystemWfpBlockLeak"].([]any)
	if len(leaks) == 0 {
		t.Fatal("no WFP leak protection")
	}

	// Proxy mode: untouched.
	adapted, errText = adaptConfig(testBaseConfig, testOptions(testBaseConfig), dir)
	if errText != "" {
		t.Fatalf("adaptConfig proxy mode: %s", errText)
	}
	settings = inboundsOf(t, decode(t, adapted))["tun"]
	if _, ok := settings["autoSystemRoutingTable"]; ok {
		t.Fatal("proxy mode must not get TUN system settings")
	}
}

// applyPerAppRules inserts the process rules before the catch-all.
func TestPerAppRules(t *testing.T) {
	exclude, errText := adaptConfig(testBaseConfig, &startOptions{
		Config: testBaseConfig, PerAppMode: "exclude", MTU: 1500,
		Apps: []string{"chrome.exe", "Firefox"},
	}, t.TempDir())
	if errText != "" {
		t.Fatalf("exclude: %s", errText)
	}
	rules := rulesOf(t, decode(t, exclude))
	inserted := rules[len(rules)-2]
	if inserted["outboundTag"] != "direct" {
		t.Fatalf("exclude rule must be direct: %v", inserted["outboundTag"])
	}
	procs, _ := inserted["process"].([]any)
	// .exe is stripped, casing normalized.
	if len(procs) != 2 || procs[0] != "chrome" || procs[1] != "firefox" {
		t.Fatalf("process list wrong: %v", procs)
	}
	catchAll := rules[len(rules)-1]
	if catchAll["outboundTag"] != "proxy-1" {
		t.Fatalf("catch-all must keep the proxy: %v", catchAll["outboundTag"])
	}

	include, errText := adaptConfig(testBaseConfig, &startOptions{
		Config: testBaseConfig, PerAppMode: "include", MTU: 1500,
		Apps: []string{"chrome"},
	}, t.TempDir())
	if errText != "" {
		t.Fatalf("include: %s", errText)
	}
	rules = rulesOf(t, decode(t, include))
	inserted = rules[len(rules)-2]
	catchAll = rules[len(rules)-1]
	if inserted["outboundTag"] != "proxy-1" {
		t.Fatalf("include rule must take the catch-all's target: %v", inserted["outboundTag"])
	}
	if catchAll["outboundTag"] != "direct" {
		t.Fatalf("catch-all must become direct in include mode: %v", catchAll["outboundTag"])
	}
}

// TrafficMeter classifies Xray's counters like TrafficMeter.kt.
func TestTrafficMeter(t *testing.T) {
	var meter TrafficMeter
	meter.sample(func() string {
		return "proxy-1,uplink,100;proxy-1,downlink,900;direct,uplink,10;direct,downlink,20;" +
			"block,uplink,5;chain-2,uplink,50;exit,downlink,60;"
	})
	rate, total := meter.snapshot()
	if rate.ProxyUp != 100 || rate.ProxyDown != 900 {
		t.Fatalf("proxy rate wrong: %+v", rate)
	}
	if rate.DirectUp != 10 || rate.DirectDown != 20 {
		t.Fatalf("direct rate wrong: %+v", rate)
	}
	// A second sample accumulates into the totals.
	meter.sample(func() string { return "proxy-1,uplink,1;" })
	_, total = meter.snapshot()
	if total.ProxyUp != 101 || total.ProxyDown != 900 {
		t.Fatalf("totals wrong: %+v", total)
	}
}

// VerificationState retries with a growing backoff (VerificationState.kt).
func TestVerificationState(t *testing.T) {
	var v verificationState
	v.start()
	g := v.request()
	if !v.begin(g) {
		t.Fatal("begin must succeed once")
	}
	if v.begin(g) {
		t.Fatal("begin must not run twice")
	}
	if v.finish(g, true) != verificationConnected {
		t.Fatal("success must connect")
	}
	g = v.request()
	_ = v.begin(g)
	first := v.finish(g, false)
	if first != verificationRetry {
		t.Fatalf("failure must retry: %v", first)
	}
	if v.lastDelayValue() != 2*time.Second {
		t.Fatalf("first retry must be 2s: %v", v.lastDelayValue())
	}
	v.stop()
	if v.finish(g, true) != verificationIgnored {
		t.Fatal("after stop everything is ignored")
	}
}

// The Tor bridge helpers transform lyrebird's lines (TorExit.kt).
func TestSnowflakeAmpBridge(t *testing.T) {
	bridge := snowflakeAmpBridge("snowflake 1.2.3.4:80 fingerprint url=https://onion.example front=foo")
	if strings.Contains(bridge, "url=https://onion.example") || strings.Contains(bridge, "front=foo") {
		t.Fatalf("rendezvous args not replaced: %s", bridge)
	}
	if !strings.Contains(bridge, "url=https://snowflake-broker.torproject.net/") {
		t.Fatalf("default rendezvous missing: %s", bridge)
	}
	if !strings.Contains(bridge, "1.2.3.4:80 fingerprint") {
		t.Fatalf("bridge head lost: %s", bridge)
	}
}

// saveGeoFiles refuses files whose checksum doesn't match.
func TestSaveGeoFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]struct {
		Data   []byte `json:"data"`
		SHA256 string `json:"sha256"`
	}{
		"geoip.dat":   {Data: []byte("geo"), SHA256: "deadbeef"},
		"geosite.dat": {Data: []byte("site"), SHA256: "feedface"},
	}
	if msg := saveGeoFiles(dir, files); msg == "" {
		t.Fatal("bad checksums must be refused")
	}
	// Correct checksums (sha256 of the contents, hex).
	checksum := func(data string) string {
		sum := sha256.Sum256([]byte(data))
		return hex.EncodeToString(sum[:])
	}
	files = map[string]struct {
		Data   []byte `json:"data"`
		SHA256 string `json:"sha256"`
	}{
		"geoip.dat":   {Data: []byte("geo"), SHA256: checksum("geo")},
		"geosite.dat": {Data: []byte("site"), SHA256: checksum("site")},
	}
	if msg := saveGeoFiles(dir, files); msg != "" {
		t.Fatalf("good files refused: %s", msg)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "geoip.dat")); err != nil || string(data) != "geo" {
		t.Fatalf("geoip.dat not saved: %v", err)
	}
}

// decodeQr reads a QR code from a PNG (the Windows camera replacement).
func TestDecodeQr(t *testing.T) {
	// A QR code of "bpb" from the gozxing encoder.
	png := testQrPng()
	text, errText := decodeQr(base64.StdEncoding.EncodeToString(png))
	if errText != "" {
		t.Fatalf("decodeQr: %s", errText)
	}
	if text != "bpb" {
		t.Fatalf("decoded %q, want %q", text, "bpb")
	}
	// Not a QR code: a clear error.
	if _, errText := decodeQr("bm90IGFuIGltYWdl"); errText == "" {
		t.Fatal("garbage must be refused")
	}
}
