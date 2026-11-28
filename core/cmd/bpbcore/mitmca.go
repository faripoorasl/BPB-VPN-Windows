// Domain Fronting's CA files (MitmCa.kt): saved in the data dir, installed into the user's
// certificate store with certutil (no elevation needed for the user store).
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func mitmCaCertPath(dir string) string { return filepath.Join(dir, "bpb-cert.crt") }
func mitmCaKeyPath(dir string) string  { return filepath.Join(dir, "bpb-cert.key") }

func mitmCaExists(dir string) bool {
	_, errCert := os.Stat(mitmCaCertPath(dir))
	_, errKey := os.Stat(mitmCaKeyPath(dir))
	return errCert == nil && errKey == nil
}

func saveMitmCa(dir, cert, key string) string {
	if err := os.WriteFile(mitmCaCertPath(dir), []byte(cert), 0o644); err != nil {
		return err.Error()
	}
	if err := os.WriteFile(mitmCaKeyPath(dir), []byte(key), 0o600); err != nil {
		return err.Error()
	}
	return ""
}

// installMitmCa installs the saved certificate; the result describes what happened, like
// MitmCa.install's message to the user.
func installMitmCa(dir string) string {
	if runtime.GOOS != "windows" {
		return "Certificate installation is available on Windows only."
	}
	cert := mitmCaCertPath(dir)
	if _, err := os.Stat(cert); err != nil {
		return "The certificate has not been created yet."
	}
	out, err := exec.Command("certutil", "-user", "-addstore", "Root", cert).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return message
	}
	return "The certificate was installed into the user's trusted root store."
}
