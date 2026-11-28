// The service: the Windows equivalent of CoreManager.kt + BpbVpnService.kt. It owns the VPN
// connection state, starts and stops Xray and the exit processes, verifies the connection,
// samples traffic once a second and broadcasts every state change to the connected app.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	xray "bpb/core/xray"
)

// The connection states, the app's CoreManager.State.
const (
	stateDisconnected  = "disconnected"
	stateConnecting    = "connecting"
	stateConnected     = "connected"
	stateDisconnecting = "disconnecting"
)

// defaultTestURL is CoreManager.DEFAULT_TEST_URL.
const defaultTestURL = "https://www.gstatic.com/generate_204"

// startOptions carries everything VpnChannel.start passes (see ConfigStore.Options in
// CoreManager.kt); the config itself is the Xray JSON the app built.
type startOptions struct {
	Config           string   `json:"config"`
	Remark           string   `json:"remark"`
	PerAppMode       string   `json:"perAppMode"`
	Apps             []string `json:"apps"`
	MTU              int      `json:"mtu"`
	AppendHTTPProxy  bool     `json:"appendHttpProxy"`
	ProxyOnly        bool     `json:"proxyOnly"`
	Exit             *string  `json:"exit"`
	ExitDirect       bool     `json:"exitDirect"`
	ExitRegion       string   `json:"exitRegion"`
	TorBridge        *string  `json:"torBridge"`
	TorCustomBridges []string `json:"torCustomBridges"`
	PsiphonConfig    *string  `json:"psiphonConfig"`
	SSHConfig        *string  `json:"sshConfig"`
	BrowserDialer    bool     `json:"browserDialer"`
	VerifyConnection bool     `json:"verifyConnection"`
}

// stateEvent is one bpb/state event, the app's VpnStateEvent.
type stateEvent struct {
	State       string  `json:"state"`
	Error       *string `json:"error"`
	ConnectedAt int64   `json:"connectedAt"`
	Detail      *string `json:"detail"`
}

// Service is the process-wide owner of the core and the connection state.
type Service struct {
	mu       sync.Mutex
	state    string
	lastErr  string
	joinedAt int64
	detail   string
	clients  map[*client]struct{}

	dir     string // the data dir (config.json, logs, tor/, psiphon/, geo files)
	binDir  string // the dir of the bpbcore binary (psiphon.exe, tor/, resources/)
	core    *xray.Controller
	scanner *xray.ScannerBridge

	// The exit ("psiphon" / "tor"), its readiness and its detail line.
	exit      *exitProcess
	setupTor  *setupTor
	verifier  *verificationState
	tickerOn  bool
	traffic   TrafficMeter
	stopReq   bool
	lastState stateEvent
}

func newService(dir, binDir string) *Service {
	return &Service{
		state:    stateDisconnected,
		clients:  map[*client]struct{}{},
		dir:      dir,
		binDir:   binDir,
		core:     xray.NewController(),
		scanner:  xray.NewScannerBridge(),
		verifier: &verificationState{},
	}
}

// initEnv points Xray at the data dir for its geo files and certificates, copying the bundled
// ones there on first run and after every app update (CoreManager.initEnv).
func (s *Service) initEnv() {
	copyGeoAssets(s.dir, s.binDir)
	xray.InitEnv(s.dir)
}

func (s *Service) addClient(c *client) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	current := s.lastState
	s.mu.Unlock()
	// The first event is the current state (like the Android EventChannel's onListen).
	c.sendJSON(map[string]any{"event": "state", "state": current.State,
		"error": current.Error, "connectedAt": current.ConnectedAt, "detail": current.Detail})
}

func (s *Service) removeClient(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

// broadcast sends an event to every connected client.
func (s *Service) broadcast(v any) {
	s.mu.Lock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		c.sendJSON(v)
	}
}

func (s *Service) setState(state, errText string) {
	s.mu.Lock()
	s.state = state
	s.lastErr = errText
	s.detail = ""
	var errPtr *string
	if errText != "" {
		errPtr = &errText
	}
	if state == stateConnected && s.joinedAt == 0 {
		s.joinedAt = time.Now().UnixMilli()
	}
	if state == stateDisconnected {
		s.joinedAt = 0
	}
	event := stateEvent{State: state, Error: errPtr, ConnectedAt: s.joinedAt}
	if state == stateDisconnected {
		event.ConnectedAt = 0
	}
	s.lastState = event
	clients := len(s.clients)
	s.mu.Unlock()
	logf("state → %s%s (%d client(s))", state, errNote(errText), clients)
	s.broadcast(map[string]any{"event": "state", "state": event.State,
		"error": event.Error, "connectedAt": event.ConnectedAt, "detail": event.Detail})
}

func errNote(err string) string {
	if err == "" {
		return ""
	}
	return " (" + err + ")"
}

// setDetail updates the exit's detail line ("Tor 45% · …"), keeping the state (CoreManager.setDetail).
func (s *Service) setDetail(text string) {
	s.mu.Lock()
	if s.detail == text {
		s.mu.Unlock()
		return
	}
	s.detail = text
	state := s.state
	errText := s.lastErr
	joinedAt := s.joinedAt
	var errPtr *string
	if errText != "" {
		errPtr = &errText
	}
	var detailPtr *string
	if text != "" {
		detailPtr = &text
	}
	s.mu.Unlock()
	s.broadcast(map[string]any{"event": "state", "state": state,
		"error": errPtr, "connectedAt": joinedAt, "detail": detailPtr})
}

// handle answers one app request; the error is the message string the app shows.
func (s *Service) handle(method string, params json.RawMessage) (any, string) {
	switch method {
	case "ping":
		return map[string]any{"ok": true}, ""
	case "shutdown":
		s.stop()
		go func() {
			time.Sleep(300 * time.Millisecond)
			os.Exit(0)
		}()
		return map[string]any{"ok": true}, ""
	case "exit":
		go func() {
			time.Sleep(300 * time.Millisecond)
			os.Exit(0)
		}()
		return map[string]any{"ok": true}, ""
	case "hostInfo":
		return map[string]any{
			"hostVersion": hostVersion,
			"xray":        xrayVersion(),
			"dataDir":     s.dir,
			"elevated":    isElevated(),
			"wintunFound": wintunFound(s.binDir),
			"platform":    runtimeOS(),
		}, ""
	case "prepare":
		var p struct {
			VPN bool `json:"vpn"`
		}
		_ = json.Unmarshal(params, &p)
		// Windows has no VPN permission dialog: the check is the admin right the TUN
		// needs and the presence of the wintun driver (the app elevates on demand).
		return map[string]any{
			"ok":          true,
			"elevated":    isElevated(),
			"wintunFound": wintunFound(s.binDir),
		}, ""
	case "supportsAppendHttpProxy":
		return true, ""
	case "start":
		var opts startOptions
		if err := json.Unmarshal(params, &opts); err != nil {
			return nil, "start options: " + err.Error()
		}
		if opts.Config == "" {
			return nil, "config is required"
		}
		if !opts.ProxyOnly && !isElevated() {
			return nil, "elevation required"
		}
		// The Android service's start returns at once; failures come through the state
		// events (the start itself runs like BpbVpnService's worker thread).
		go func() {
			s.start(&opts)
		}()
		return nil, ""
	case "stop":
		s.stop()
		return nil, ""
	case "state":
		s.mu.Lock()
		event := s.lastState
		s.mu.Unlock()
		return event, ""
	case "readLog":
		var p struct {
			Log string `json:"log"`
		}
		_ = json.Unmarshal(params, &p)
		return tailLog(s.dir, logName(p.Log)), ""
	case "logSizes":
		return logSizes(s.dir), ""
	case "clearLog":
		clearLogs(s.dir)
		return nil, ""
	case "versions":
		return s.versions(), ""
	case "saveGeoAssets":
		var p struct {
			Files map[string]struct {
				Data   []byte `json:"data"`
				SHA256 string `json:"sha256"`
			} `json:"files"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, "geo assets: " + err.Error()
		}
		if errText := saveGeoFiles(s.dir, p.Files); errText != "" {
			return nil, errText
		}
		return nil, ""
	case "appInfo":
		return map[string]any{"version": appVersion(), "abis": []string{"x64"}}, ""
	case "measureDelay":
		var p struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(params, &p)
		if p.URL == "" {
			p.URL = defaultTestURL
		}
		if !s.core.IsRunning() {
			return nil, "Not connected"
		}
		ms, err := s.core.MeasureDelay(p.URL)
		if err != nil {
			return nil, err.Error()
		}
		return ms, ""
	case "getMyIp":
		var p struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(params, &p)
		if !s.core.IsRunning() {
			return nil, "Not connected"
		}
		ip, err := s.core.GetMyIp(p.URL)
		if err != nil {
			return nil, err.Error()
		}
		return map[string]any{"ip": ip.IP, "country_code": ip.CountryCode,
			"organization_name": ip.ISP}, ""
	case "sshHostKey":
		return s.core.SSHHostKey(), ""
	case "trafficStats":
		s.mu.Lock()
		state := s.state
		s.mu.Unlock()
		if state != stateConnected {
			return nil, ""
		}
		rate, total := s.traffic.snapshot()
		return map[string]any{
			"proxyUp": rate.ProxyUp, "proxyDown": rate.ProxyDown,
			"directUp": rate.DirectUp, "directDown": rate.DirectDown,
			"proxyUpTotal": total.ProxyUp, "proxyDownTotal": total.ProxyDown,
		}, ""
	case "installedApps":
		return installedApps(), ""
	case "mitmCaExists":
		return mitmCaExists(s.dir), ""
	case "saveMitmCa":
		var p struct {
			Cert string `json:"cert"`
			Key  string `json:"key"`
		}
		_ = json.Unmarshal(params, &p)
		if errText := saveMitmCa(s.dir, p.Cert, p.Key); errText != "" {
			return nil, errText
		}
		return nil, ""
	case "installMitmCa":
		return installMitmCa(s.dir), ""
	case "scannerCidrs":
		cidrs, err := xray.GeoIPCIDRs("cloudflare")
		if err != nil {
			return nil, err.Error()
		}
		return cidrs, ""
	case "scannerStart":
		var p struct {
			Config string `json:"config"`
		}
		_ = json.Unmarshal(params, &p)
		return s.scannerStart(p.Config)
	case "scannerPause":
		return s.scanner.PauseScan(), ""
	case "scannerResume":
		return s.scanner.ResumeScan(), ""
	case "scannerStop":
		return s.scanner.StopScan(), ""
	case "startSetupTor":
		var p struct {
			Bridges []string `json:"bridges"`
		}
		_ = json.Unmarshal(params, &p)
		if errText := s.startSetupTor(p.Bridges); errText != "" {
			return nil, errText
		}
		return setupTorHTTPPort, ""
	case "setupTorStatus":
		s.mu.Lock()
		st := s.setupTor
		s.mu.Unlock()
		if st == nil {
			return map[string]any{"progress": 0, "running": false, "error": nil}, ""
		}
		progress, running, errText := st.status()
		var errPtr *string
		if errText != "" {
			errPtr = &errText
		}
		return map[string]any{"progress": progress, "running": running, "error": errPtr}, ""
	case "stopSetupTor":
		s.mu.Lock()
		st := s.setupTor
		s.mu.Unlock()
		if st != nil {
			st.stop()
		}
		return nil, ""
	case "startEchGateway":
		if errText := xray.StartEchGateway(); errText != "" {
			return nil, errText
		}
		return nil, ""
	case "checkDns":
		var p struct {
			Request string `json:"request"`
		}
		_ = json.Unmarshal(params, &p)
		return xray.CheckDNSJSON(p.Request), ""
	case "openUrl":
		var p struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(params, &p)
		return openURL(p.URL), ""
	case "setSystemProxy":
		// appendHttpProxy: the system proxy points at Xray's mixed inbound while connected.
		var p struct {
			Enable bool `json:"enable"`
		}
		_ = json.Unmarshal(params, &p)
		if errText := setSystemProxy(p.Enable, localProxyPort); errText != "" {
			return nil, errText
		}
		return nil, ""
	case "decodeQr":
		var p struct {
			Image string `json:"image"`
		}
		_ = json.Unmarshal(params, &p)
		text, errText := decodeQr(p.Image)
		if errText != "" {
			return nil, errText
		}
		return map[string]any{"text": text}, ""
	default:
		return nil, "unknown method: " + method
	}
}

// scannerStart mirrors MainActivity's scannerStart: the scanner and the VPN cannot run at
// once, and the events are broadcast as {"event":"scanner",…}.
func (s *Service) scannerStart(config string) (any, string) {
	s.mu.Lock()
	state := s.state
	scannerRunning := s.scanner.IsRunning()
	s.mu.Unlock()
	if state != stateDisconnected {
		s.stop()
		deadline := time.Now().Add(30 * time.Second)
		for {
			s.mu.Lock()
			state = s.state
			s.mu.Unlock()
			if state == stateDisconnected || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if scannerRunning {
		return nil, "Could not start scanner: a scan is already running"
	}
	s.initEnv()
	if errText := s.scanner.StartScan(config); errText != "" {
		return nil, errText
	}
	// Relay the scanner's events until it is done (MainActivity's poller).
	go func() {
		for s.scanner.IsRunning() {
			payload := s.scanner.PollEvent(500)
			if payload == "" {
				continue
			}
			var event struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				s.broadcast(map[string]any{"event": "scanner", "payload": `{"type":"error","message":"could not encode scanner event"}`})
				return
			}
			s.broadcast(map[string]any{"event": "scanner", "payload": payload})
			switch event.Type {
			case "done", "stopped", "error":
				return
			}
		}
	}()
	return nil, ""
}

// start is BpbVpnService.startVpn: the state goes Connecting, Xray (and the exit) start, and
// the connection is verified before it is reported Connected. The returned string is the
// failure message, or "" on success.
func (s *Service) start(opts *startOptions) string {
	// Restarting with a new config: tear down the previous session first (the Android
	// service's startVpn does the same via shutdownCore).
	s.stop()
	s.mu.Lock()
	s.stopReq = false
	s.mu.Unlock()
	s.setState(stateConnecting, "")

	if errText := s.startSync(opts); errText != "" {
		s.stopSync()
		s.setState(stateDisconnected, errText)
		return errText
	}
	return ""
}

// startSync does the work of start; it blocks until the connection is ready (or failed),
// like BpbVpnService's worker thread.
func (s *Service) startSync(opts *startOptions) string {
	s.initEnv()
	s.stopSync() // restarting with a new config: tear down the previous session first
	clearLogs(s.dir)

	config := attachLogs(opts.Config, s.dir)

	// Before the exit, which may connect through it.
	if opts.BrowserDialer {
		startBrowserDialer(browserDialerAddress)
	} else {
		stopBrowserDialer()
	}
	if opts.SSHConfig != nil && *opts.SSHConfig != "" {
		if err := s.core.ConfigureSSH(*opts.SSHConfig); err != "" {
			return err
		}
	} else if err := s.core.ConfigureSSH(""); err != "" {
		return err
	}

	// The config the app built is the same on every platform; bpbcore adds the Windows-only
	// parts: the system routes and DNS of the TUN, the per-app process rules and the log paths.
	config, errText := adaptConfig(config, opts, s.dir)
	if errText != "" {
		return errText
	}
	if errText := saveLastConfig(s.dir, config); errText != "" {
		logf("saving the last config failed: %v", errText)
	}

	// fd 0: the core creates its own TUN device from the config's tun inbound on Windows
	// (wintun); on Android the VPN service's fd is passed instead.
	if err := s.core.Start(config, 0); err != nil {
		return coreStartMessage(err)
	}

	// The exit (Psiphon / Tor) starts once Xray is up, since it reaches its servers through
	// Xray's psiphon-in / tor-in inbound.
	if opts.Exit != nil && *opts.Exit != "" {
		if errText := s.startExit(*opts.Exit, opts); errText != "" {
			return errText
		}
	}

	if opts.VerifyConnection {
		s.verify(opts)
		return ""
	}
	s.onCoreStarted(opts)
	return ""
}

// startExit is BpbVpnService.startExit: one Psiphon or Tor exit at a time.
func (s *Service) startExit(exit string, opts *startOptions) string {
	exitName := exit
	onExit := func(message string) {
		logf("exit (%s) stopped: %s", exitName, message)
		s.mu.Lock()
		stopped := s.stopReq
		s.mu.Unlock()
		if !stopped && s.currentState() != stateDisconnected {
			s.stopSync()
			s.setState(stateDisconnected, message)
		}
	}
	switch exit {
	case "psiphon":
		s.exit = startPsiphon(s.dir, s.binDir, opts.ExitRegion, opts.ExitDirect, opts.PsiphonConfig, s.detailSink(), onExit)
		return s.exit.error()
	case "tor":
		s.exit = startTor(s.dir, s.binDir, opts.ExitRegion, opts.ExitDirect, opts.TorBridge, opts.TorCustomBridges, s.detailSink(), onExit)
		return s.exit.error()
	case "ssh":
		return "" // the SSH SOCKS is owned by the Go controller and started with Xray
	default:
		return "Unknown exit: " + exit
	}
}

// detailSink returns the callback the exits use for the home page's detail line.
func (s *Service) detailSink() func(string) { return s.setDetail }

func (s *Service) currentState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// onCoreStarted is BpbVpnService.onCoreStarted: traffic sampling on, state Connected.
func (s *Service) onCoreStarted(opts *startOptions) {
	s.mu.Lock()
	s.traffic.reset()
	s.tickerOn = true
	s.mu.Unlock()
	go s.ticker()
	s.setState(stateConnected, "")
	if opts.AppendHTTPProxy && !opts.ProxyOnly {
		// The system proxy follows the tunnel while it is up (Android sets it on the
		// VPN network; on Windows it is the global WinINET proxy).
		if errText := setSystemProxy(true, localProxyPort); errText != "" {
			logf("system proxy: %s", errText)
		}
	}
}

// ticker samples traffic once a second while connecting or connected (BpbVpnService's ticker).
func (s *Service) ticker() {
	for {
		s.mu.Lock()
		on := s.tickerOn
		s.mu.Unlock()
		if !on {
			return
		}
		state := s.currentState()
		if state == stateConnected || state == stateConnecting {
			s.traffic.sample(func() string { return s.core.OutboundTraffic() })
		}
		time.Sleep(time.Second)
	}
}

// verify is the service's verification loop (BpbVpnService.checkConnection + VerificationState):
// a delay test through the tunnel, retried with a growing backoff until it succeeds.
func (s *Service) verify(opts *startOptions) {
	s.verifier.start()
	s.setDetail("Checking connection…")
	go func() {
		delay := time.Duration(0)
		for {
			generation := s.verifier.request()
			if !s.verifier.begin(generation) {
				return
			}
			if delay > 0 {
				time.Sleep(delay)
			}
			if s.currentState() == stateDisconnected {
				s.verifier.stop()
				return
			}
			ms := int64(-1)
			var err error
			if s.core.IsRunning() {
				ms, err = s.core.MeasureDelay(defaultTestURL)
			} else {
				err = errors.New("core stopped")
			}
			switch s.verifier.finish(generation, err == nil && ms >= 0) {
			case verificationConnected:
				s.mu.Lock()
				s.traffic.reset()
				s.tickerOn = true
				s.mu.Unlock()
				go s.ticker()
				s.setDetail("")
				s.setState(stateConnected, "")
				if opts.AppendHTTPProxy && !opts.ProxyOnly {
					_ = setSystemProxy(true, localProxyPort)
				}
				return
			case verificationRetry:
				logf("connection check failed: %v; retrying", err)
				s.setDetail("Failed · Retrying…")
				delay = s.verifier.lastDelayValue()
			default: // ignored: the service is stopping
				return
			}
		}
	}()
}

// stop is BpbVpnService.stopVpn: DISCONNECTING → everything down → DISCONNECTED.
func (s *Service) stop() {
	s.mu.Lock()
	if s.state == stateDisconnected {
		s.mu.Unlock()
		return
	}
	s.stopReq = true
	s.mu.Unlock()
	s.setState(stateDisconnecting, "")
	s.stopSync()
	s.setState(stateDisconnected, "")
}

// stopSync tears everything down (BpbVpnService.shutdownCore).
func (s *Service) stopSync() {
	s.mu.Lock()
	s.verifier.stop()
	s.tickerOn = false
	s.mu.Unlock()
	if s.setupTor != nil {
		s.setupTor.stop()
	}
	if s.exit != nil {
		s.exit.stop()
		s.exit = nil
	}
	stopBrowserDialer()
	_ = s.core.Stop()
	clearSystemProxy()
	s.mu.Lock()
	s.traffic.reset()
	s.mu.Unlock()
}

// versions is CoreManager.versions: the cores report themselves.
func (s *Service) versions() map[string]string {
	return map[string]string{
		"xray":    xrayVersion(),
		"psiphon": binaryOutput(filepath.Join(s.binDir, "psiphon"+exeExt()), "-version"),
		"tor":     binaryOutput(filepath.Join(s.binDir, "tor", "tor"+exeExt()), "--version"),
	}
}

func binaryOutput(path string, args ...string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	cmd := exec.Command(path, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// coreStartMessage turns the controller's error into the message the app shows (the Android
// layer lets it bubble as the failure text).
func coreStartMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "bind"), strings.Contains(msg, "address already in use"):
		return "Another process is using the local proxy ports. Close it and try again."
	case strings.Contains(msg, "Access is denied"), strings.Contains(msg, "access denied"):
		return "Administrator rights are required: " + msg
	}
	return msg
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("2006/01/02 15:04:05")+" "+format+"\n", args...)
}

// appVersion is the app's version from pubspec.yaml, baked in at build time.
var bakedAppVersion string

func appVersion() string {
	if bakedAppVersion != "" {
		return bakedAppVersion
	}
	return hostVersion
}

func runtimeOS() string { return runtime.GOOS }
