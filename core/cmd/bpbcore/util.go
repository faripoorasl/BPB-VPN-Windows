// Paths, bundled assets and the logs (CoreLog.kt). The data dir is the Windows equivalent of
// Android's filesDir; the bin dir is where the host binary sits (with psiphon.exe, tor/ and
// the bundled resources next to it).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	xray "bpb/core/xray"
)

// xrayCoreVersion is the Xray-core version, e.g. 26.10.21.
func xrayCoreVersion() string { return xray.Version() }

// localProxyPort is XrayConfigBuilder.localProxyPort (the mixed inbound).
const localProxyPort = 10808

// dataDir is %LOCALAPPDATA%\BPBVPN\core on Windows (filesDir's equivalent; the app's
// rendezvous file lives there — os.UserConfigDir would be the Roaming profile, which the
// app never reads), the XDG config dir elsewhere; the tests override it with
// BPBVPN_DATA_DIR.
func dataDir() (string, error) {
	if dir := os.Getenv("BPBVPN_DATA_DIR"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "windows" {
		root := os.Getenv("LOCALAPPDATA")
		if root == "" {
			root = `C:\BPBVPN`
		}
		return filepath.Join(root, "BPBVPN", "core"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "BPBVPN", "core"), nil
}

// exeDir is the dir of the running binary.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func xrayVersion() string { return xrayCoreVersion() }

// region geo assets (CoreManager.copyGeoAssets / saveGeoAssets)

// geoFiles are Xray's geo files and Tor's GeoIP files, copied from the bundled resources on
// first run and after every update.
var geoFiles = []string{"geoip.dat", "geosite.dat", "tor_geoip", "tor_geoip6"}

// bundledGeoFiles maps a geo file to where the installer puts it.
func bundledGeoFile(binDir, name string) string {
	return filepath.Join(binDir, "resources", "geo", name)
}

func copyGeoAssets(dir, binDir string) {
	marker := filepath.Join(dir, ".geo-copied")
	bundled := geoFiles
	// Re-copy when the bundled set is newer than the marker.
	newest := int64(0)
	for _, name := range bundled {
		if st, err := os.Stat(bundledGeoFile(binDir, name)); err == nil && st.ModTime().Unix() > newest {
			newest = st.ModTime().Unix()
		}
	}
	if st, err := os.Stat(marker); err == nil && st.ModTime().Unix() >= newest && newest > 0 {
		return
	}
	for _, name := range bundled {
		copyFile(bundledGeoFile(binDir, name), filepath.Join(dir, name))
	}
	os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0o644)
}

func copyFile(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(dst)
		os.Rename(tmp, dst)
	}
}

// saveGeoFiles replaces Xray's geo files (Settings → Routing → Update geo assets): none is
// replaced unless every one matches its checksum (CoreManager.saveGeoAssets).
func saveGeoFiles(dir string, files map[string]struct {
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}) string {
	if len(files) == 0 {
		return "No geo files"
	}
	for name, file := range files {
		if name != "geoip.dat" && name != "geosite.dat" {
			return "Unknown geo files"
		}
		sum := sha256.Sum256(file.Data)
		if hex.EncodeToString(sum[:]) != strings.ToLower(file.SHA256) {
			return fmt.Sprintf("%s doesn't match its checksum", name)
		}
	}
	for name, file := range files {
		tmp := filepath.Join(dir, name+".tmp")
		if err := os.WriteFile(tmp, file.Data, 0o644); err != nil {
			return err.Error()
		}
		dst := filepath.Join(dir, name)
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(dst)
			if err := os.Rename(tmp, dst); err != nil {
				return err.Error()
			}
		}
	}
	return ""
}

// endregion

// region logs (CoreLog.kt)

func logName(log string) string {
	switch log {
	case "tor", "psiphon":
		return log
	default:
		return "xray"
	}
}

func logFile(dir, log string) string { return filepath.Join(dir, logName(log)+".log") }

// attachLogs points the config's error and access logs at the Xray log (CoreLog.attach).
func attachLogs(config, dir string) string {
	var json_ map[string]any
	if err := json.Unmarshal([]byte(config), &json_); err != nil {
		return config
	}
	logSection, ok := json_["log"].(map[string]any)
	if !ok {
		logSection = map[string]any{}
		json_["log"] = logSection
	}
	path := logFile(dir, "xray")
	logSection["error"] = path
	logSection["access"] = path
	data, err := json.Marshal(json_)
	if err != nil {
		return config
	}
	return string(data)
}

// saveLastConfig persists the config so a restarted service can pick it up.
func saveLastConfig(dir, config string) string {
	return writeText(filepath.Join(dir, "config.json"), config)
}

func writeText(path, content string) string {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err.Error()
	}
	return ""
}

func clearLogs(dir string) {
	for _, log := range []string{"xray", "tor", "psiphon"} {
		os.WriteFile(filepath.Join(dir, log+".log"), []byte(""), 0o644)
	}
}

func logSizes(dir string) map[string]any {
	out := map[string]any{}
	for _, log := range []string{"xray", "tor", "psiphon"} {
		size := int64(0)
		if st, err := os.Stat(filepath.Join(dir, log+".log")); err == nil {
			size = st.Size()
		}
		out[log] = size
	}
	return out
}

// tailLog is CoreLog.tail: the last 64 KB, starting at a whole line.
func tailLog(dir, log string) string {
	return tailFile(logFile(dir, log), 64*1024)
}

func tailFile(path string, maxBytes int64) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if int64(len(data)) > maxBytes {
		data = data[int64(len(data))-maxBytes:]
	}
	text := string(data)
	if int64(len(text)) == maxBytes {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text
}

// appendLog adds one of the service's own status lines to a log, in Xray's line format.
func appendLog(dir, log, message string) {
	// UTC, like Xray's own lines (Go on Android has no local time zone).
	timestamp := time.Now().UTC().Format("2006/01/02 15:04:05.000000")
	f, err := os.OpenFile(filepath.Join(dir, logName(log)+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s [App] %s\n", timestamp, message)
}

// endregion
