// Command bpbcore is the Windows host of the BPB core (see core/xray): it wraps the same
// Xray controller, scanner and networking packages the Android app binds with gomobile, and
// serves them to the Flutter app over a small localhost JSON-lines protocol (see ipc.go).
//
// It is the Windows equivalent of the app's Android layer (MainActivity.kt, CoreManager.kt,
// BpbVpnService.kt and friends): one VPN connection at a time, the Psiphon / Tor exit
// processes, the traffic sampler, the logs, the geo assets and the MITM CA. VPN (TUN) mode
// needs administrator rights (the wintun adapter and the system routes), so the app relaunches
// it elevated when needed; proxy mode runs unprivileged.
//
//	bpbcore --service --info-file <path>   the service the app talks to
//	bpbcore --version                      the Xray core version
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version of the bpbcore host itself (not Xray's, see the version method).
const hostVersion = "1.3.2"

func main() {
	service := flag.Bool("service", false, "run the app-facing service")
	infoFile := flag.String("info-file", "", "where to write the listening port and token")
	flag.Parse()

	switch {
	case flag.Arg(0) == "version":
		fmt.Println(xrayVersion())
	case *service:
		if err := runService(*infoFile); err != nil {
			fmt.Fprintln(os.Stderr, "bpbcore:", err)
			os.Exit(1)
		}
	default:
		fmt.Println("BPB core host", hostVersion, "- Xray", xrayVersion())
		fmt.Println("usage: bpbcore --service --info-file <path>")
	}
}

// runService listens on a localhost port and serves the app until it is told to shut down.
// The chosen port and the token go to the info file, so the app can find the service even
// when it was started elevated (an elevated child's stdout is not reachable from the app).
func runService(infoFilePath string) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	service := newService(dir, exeDir())

	// The info file tells the app where we are; it is removed on shutdown so a dead
	// service is never reused.
	info := map[string]any{
		"port":      listener.Addr().(*net.TCPAddr).Port,
		"token":     token,
		"pid":       os.Getpid(),
		"version":   hostVersion,
		"dataDir":   dir,
		"elevated":  isElevated(),
		"startedAt": time.Now().UnixMilli(),
	}
	if infoFilePath == "" {
		infoFilePath = os.Getenv("BPBVPN_INFO_FILE")
	}
	if infoFilePath == "" {
		infoFilePath = filepath.Join(dir, "core-info.json")
	}
	if err := writeInfoFile(infoFilePath, info); err != nil {
		return err
	}
	defer os.Remove(infoFilePath)

	logf("serving on %d (data dir %s, elevated %v)", info["port"], dir, info["elevated"])

	var wg sync.WaitGroup
	for {
		conn, err := listener.Accept()
		if err != nil {
			wg.Wait()
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveConn(conn, token, service)
		}()
	}
}

// writeInfoFile writes the rendezvous info atomically (tmp file + rename), so the app never
// reads a half-written file while an elevated instance is starting.
func writeInfoFile(path string, info map[string]any) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
