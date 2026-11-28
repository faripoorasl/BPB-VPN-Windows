// The installed-app list for Settings → Per-app proxy (MainActivity.installedApps on Windows):
// the machine's programs, found where Windows installs them. The entries' package is the
// executable name, which is what the routing rules' process matcher sees.
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// installedApps scans the standard install locations. `system` marks the machine-wide
// locations (Program Files), the rest are per-user installs; icons are always null (the UI
// shows its fallback icon).
func installedApps() []map[string]any {
	var apps []map[string]any
	seen := map[string]bool{}

	add := func(path, label string, system bool) {
		base := filepath.Base(path)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		key := strings.ToLower(name)
		if key == "" || seen[key] || key == "bpbcore" || key == "bpb" {
			return
		}
		// Uninstallers and helpers are not apps anyone routes.
		lower := strings.ToLower(name)
		for _, junk := range []string{"uninstall", "unins", "setup", "crash", "updater", "helper", "installer"} {
			if strings.Contains(lower, junk) {
				return
			}
		}
		seen[key] = true
		apps = append(apps, map[string]any{
			"package": name,
			"label":   label,
			"system":  system,
			"icon":    nil,
		})
	}

	scanExes := func(root string, depth int, system bool) {
		entries, err := os.ReadDir(root)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				name := f.Name()
				if !strings.EqualFold(filepath.Ext(name), ".exe") {
					continue
				}
				// The exe's label is its folder's, falling back to the file's base name.
				label := strings.TrimSuffix(name, ".exe")
				if label == entry.Name() || len(files) == 1 {
					label = entry.Name()
				}
				add(filepath.Join(dir, name), label, system)
				break // one app per folder: the first exe
			}
			if depth > 1 {
				// One more level, for layouts like Company\App\app.exe.
				scanNested(dir, &apps, seen, system)
			}
		}
	}

	home, err := os.UserHomeDir()
	if err == nil {
		// Per-user installs first, then the machine-wide ones.
		scanExes(filepath.Join(home, "AppData", "Local", "Programs"), 1, false)
		scanExes(os.Getenv("ProgramFiles"), 1, true)
		scanExes(os.Getenv("ProgramFiles(x86)"), 1, true)
	}

	sort.Slice(apps, func(i, j int) bool {
		a, _ := apps[i]["label"].(string)
		b, _ := apps[j]["label"].(string)
		return strings.ToLower(a) < strings.ToLower(b)
	})
	if len(apps) > 500 {
		apps = apps[:500]
	}
	return apps
}

// scanNested handles Company\App\app.exe layouts.
func scanNested(dir string, apps *[]map[string]any, seen map[string]bool, system bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sub := filepath.Join(dir, entry.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".exe") {
				continue
			}
			name := strings.TrimSuffix(f.Name(), ".exe")
			key := strings.ToLower(name)
			if seen[key] {
				break
			}
			seen[key] = true
			*apps = append(*apps, map[string]any{
				"package": name,
				"label":   entry.Name(),
				"system":  system,
				"icon":    nil,
			})
			break
		}
	}
}
