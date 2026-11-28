// The exit processes, Psiphon and Tor (ExitProcess.kt, PsiphonExit.kt, TorExit.kt,
// SetupTor.kt): one at a time, each serving a SOCKS5 proxy (and DNS) for Xray's exit
// outbound, reaching its own servers through Xray's psiphon-in / tor-in inbound — or
// directly (the Psiphon / Tor connection types), like on Android.
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The ports match XrayConfigBuilder (ExitProcess.kt).
const (
	exitUpstreamPort   = 10828 // Xray's psiphon-in / tor-in inbound
	exitSocksPort      = 10829 // the exit's SOCKS5 for Xray's exit outbound
	exitDNSPort        = 10830 // the exit's DNS for Xray's DNS
	torControlPort     = 10831 // bootstrap progress and circuit status
	browserDialerPort  = 10832 // Xray's browser dialer page
	setupTorHTTPPort   = 10840 // the setup wizard's Tor HTTP CONNECT proxy
	setupTorControlPrt = 10841
)

// lineKind is ExitProcess.Line: what an output line means to the service.
type lineKind int

const (
	lineNone lineKind = iota
	lineReady
	lineError
	lineStatus
)

type exitLine struct {
	kind    lineKind
	message string
}

// exitProcess is one running exit binary.
type exitProcess struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	log     string // "tor" or "psiphon"
	onExit  func(message string)
	lastErr string
	stopped bool
}

// runExitProcess runs the exit binary and blocks until it is ready, however long that takes:
// the exit keeps trying, and stop (Disconnect) is the way to give up — ExitProcess.start.
// With copyOutput the process's output is copied as is to its log (Psiphon); otherwise it
// writes its own log and the output only goes to the service's log (Tor).
func runExitProcess(dir, log string, command []string, workDir string, parse func(string) exitLine, copyOutput bool, onStarted func(), onExit func(string)) *exitProcess {
	p := &exitProcess{log: log, onExit: onExit}
	appendLog(dir, log, "starting")

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = workDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.lastErr = err.Error()
		return p
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		p.lastErr = err.Error()
		return p
	}
	if err := cmd.Start(); err != nil {
		p.lastErr = err.Error()
		return p
	}
	p.cmd = cmd

	ready := make(chan struct{})
	var readyOne sync.Once
	markReady := func() { readyOne.Do(func() { close(ready) }) }

	var out *os.File
	if copyOutput {
		if f, err := os.OpenFile(filepath.Join(dir, log+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			out = f
		}
	}
	handle := func(line string) {
		if out != nil {
			fmt.Fprintf(out, "%s\n", line)
		} else {
			logf("%s: %s", log, line)
		}
		switch l := parse(line); l.kind {
		case lineReady:
			appendLog(dir, log, "connected")
			markReady()
		case lineError:
			p.mu.Lock()
			p.lastErr = l.message
			p.mu.Unlock()
			appendLog(dir, log, l.message)
		case lineStatus:
			appendLog(dir, log, l.message)
		}
	}

	go copyLines(stdout, handle)
	go func() {
		copyLines(stderr, handle)
		// Stdout closed: the process exited.
		if out != nil {
			out.Close()
		}
		p.mu.Lock()
		wasStopped := p.stopped
		p.cmd = nil
		lastErr := p.lastErr
		p.mu.Unlock()
		markReady()
		if !wasStopped && p.onExit != nil {
			message := lastErr
			if message == "" {
				message = strings.ToUpper(log[:1]) + log[1:] + " stopped"
			}
			p.onExit(message)
		}
	}()
	if onStarted != nil {
		go onStarted()
	}

	<-ready
	// The starter returns with the exit either running or failed; the caller reports the
	// failure through error().
	p.mu.Lock()
	running := p.cmd != nil
	lastErr := p.lastErr
	p.mu.Unlock()
	if !running && lastErr != "" {
		p.stop()
	}
	return p
}

// error is "" when the exit is running; otherwise why it failed (ExitProcess.start throws).
func (p *exitProcess) error() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil && p.lastErr != "" {
		return p.lastErr
	}
	return ""
}

// stop closes stdin first (Psiphon exits when it closes) and kills the process.
func (p *exitProcess) stop() {
	p.mu.Lock()
	p.stopped = true
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil {
		return
	}
	if cmd.Stdin != nil {
		if c, ok := cmd.Stdin.(io.WriteCloser); ok {
			c.Close()
		}
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func copyLines(r io.Reader, onLine func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		onLine(scanner.Text())
	}
}

// region Psiphon (PsiphonExit.kt)

var (
	psiphonFailedRe   = regexp.MustCompile(`"message":"failed to connect to `)
	psiphonTunnelsRe  = regexp.MustCompile(`"noticeType":"Tunnels"`)
	psiphonTunnelsCnt = regexp.MustCompile(`"count":\s*(\d+)`)
)

// startPsiphon runs the bundled psiphon binary; its config is the app's psiphon_config.json
// resource plus the connection's fields (PsiphonExit.start).
func startPsiphon(dir, binDir, region string, direct bool, extra *string, setDetail func(string), onExit func(string)) *exitProcess {
	dataDir := filepath.Join(dir, "psiphon")
	os.MkdirAll(dataDir, 0o755)

	config := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(binDir, "resources", "psiphon_config.json")); err == nil {
		json.Unmarshal(raw, &config)
	}
	config["DataRootDirectory"] = dataDir
	config["EgressRegion"] = region
	if !direct {
		config["UpstreamProxyURL"] = fmt.Sprintf("socks5://127.0.0.1:%d", exitUpstreamPort)
	}
	// The binary's own servers: SOCKS5 (TCP and UDP) for Xray's exit outbound, and DNS.
	config["BpbSocksListen"] = fmt.Sprintf("127.0.0.1:%d", exitSocksPort)
	config["BpbDnsListen"] = fmt.Sprintf("127.0.0.1:%d", exitDNSPort)
	if extra != nil && *extra != "" {
		var fields map[string]any
		if json.Unmarshal([]byte(*extra), &fields) == nil {
			for k, v := range fields {
				config[k] = v
			}
		}
	}
	raw, _ := json.Marshal(config)
	configFile := filepath.Join(dataDir, "config.json")
	if err := os.WriteFile(configFile, raw, 0o644); err != nil {
		return &exitProcess{lastErr: err.Error(), log: "psiphon"}
	}

	failed := 0
	connected := false
	lost := false

	setDetail("Psiphon · Finding a server…")
	appendLog(dir, "psiphon", fmt.Sprintf("starting, region %s", orFastest(region)))

	return runExitProcess(dir, "psiphon",
		[]string{filepath.Join(binDir, "psiphon"+exeExt()), "-config", configFile},
		dataDir,
		func(line string) exitLine {
			if strings.HasPrefix(line, "{\"event\"") {
				var event struct {
					Event   string `json:"event"`
					Message string `json:"message"`
				}
				if json.Unmarshal([]byte(line), &event) == nil {
					switch event.Event {
					case "ready":
						connected = true
						setDetail("")
						return exitLine{kind: lineReady}
					case "error":
						return exitLine{kind: lineError, message: event.Message}
					}
				}
				return exitLine{}
			}
			// Psiphon's Info notice for each failed attempt: "failed to connect to <server>: <error>".
			if psiphonFailedRe.MatchString(line) {
				failed++
				if !connected {
					if failed == 1 {
						setDetail("Psiphon · 1 server failed")
					} else {
						setDetail(fmt.Sprintf("Psiphon · %d servers failed", failed))
					}
				}
				return exitLine{}
			}
			if psiphonTunnelsRe.MatchString(line) {
				if m := psiphonTunnelsCnt.FindStringSubmatch(line); m != nil {
					count := 0
					fmt.Sscanf(m[1], "%d", &count)
					// Tunnels dropped to 0 after connecting: Psiphon is reconnecting.
					if count == 0 && connected && !lost {
						lost = true
						setDetail("Psiphon · Reconnecting…")
						return exitLine{kind: lineStatus, message: "reconnecting"}
					}
					if count > 0 && lost {
						lost = false
						setDetail("")
						return exitLine{kind: lineStatus, message: "reconnected"}
					}
				}
			}
			return exitLine{}
		},
		true, // the output is copied as is to the Psiphon log
		nil,
		onExit,
	)
}

// endregion

// region Tor (TorExit.kt)

var torErrRe = regexp.MustCompile(`\[err\]`)
var torProgressRe = regexp.MustCompile(`PROGRESS=(\d+)`)
var torReasonRe = regexp.MustCompile(`REASON=(\S+)`)

// startTor runs the bundled tor with its torrc; as an exit it reaches relays through Xray's
// tor-in inbound, without bridges; direct types use the chosen bridges (TorExit.start).
func startTor(dir, binDir, region string, direct bool, bridge *string, customBridges []string, setDetail func(string), onExit func(string)) *exitProcess {
	torDir := filepath.Join(dir, "tor")
	dataDir := filepath.Join(torDir, "data")
	os.MkdirAll(dataDir, 0o755)

	torBinary := filepath.Join(binDir, "tor", "tor"+exeExt())
	lyrebird := filepath.Join(binDir, "tor", "lyrebird"+exeExt())

	snowflakeAmp := direct && bridge != nil && *bridge == "snowflake-amp"
	var bridges []string
	switch {
	case !direct || bridge == nil || *bridge == "direct" || *bridge == "":
		bridges = nil
	case *bridge == "custom":
		bridges = customBridges
		if len(bridges) == 0 {
			return &exitProcess{lastErr: "No custom bridges", log: "tor"}
		}
	case snowflakeAmp:
		for _, b := range builtInBridges(binDir, "snowflake") {
			bridges = append(bridges, snowflakeAmpBridge(b))
		}
	default:
		bridges = builtInBridges(binDir, *bridge)
	}

	viaXray := !direct
	var lines []string
	lines = append(lines,
		"DataDirectory "+dataDir,
		fmt.Sprintf("SocksPort 127.0.0.1:%d", exitSocksPort),
		// Names resolved at the exit, over an existing circuit.
		fmt.Sprintf("DNSPort 127.0.0.1:%d", exitDNSPort),
		"GeoIPFile "+filepath.Join(dir, "tor_geoip"),
		"GeoIPv6File "+filepath.Join(dir, "tor_geoip6"),
		// For the control connection: progress and circuit status, with the cookie in DataDirectory.
		fmt.Sprintf("ControlPort 127.0.0.1:%d", torControlPort),
		"CookieAuthentication 1",
		// Tor's own log, shown on the app's log page.
		"Log notice file "+filepath.Join(dir, "tor.log"),
		"ClientOnly 1",
		"AvoidDiskWrites 1",
	)
	if region != "" {
		lines = append(lines, fmt.Sprintf("ExitNodes {%s}", strings.ToLower(region)), "StrictNodes 1")
	}
	if viaXray {
		lines = append(lines, fmt.Sprintf("Socks5Proxy 127.0.0.1:%d", exitUpstreamPort))
	}
	if len(bridges) > 0 {
		lines = append(lines,
			"UseBridges 1",
			fmt.Sprintf("ClientTransportPlugin meek_lite,obfs4,webtunnel exec %s", lyrebird),
			fmt.Sprintf("ClientTransportPlugin snowflake exec %s", lyrebird),
		)
		for _, b := range bridges {
			lines = append(lines, "Bridge "+b)
		}
	}
	torrc := filepath.Join(torDir, "torrc")
	if err := os.WriteFile(torrc, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return &exitProcess{lastErr: err.Error(), log: "tor"}
	}
	// A cookie from the last run would be stale; Tor writes a new one when it starts.
	os.Remove(filepath.Join(dataDir, "control_auth_cookie"))

	via := ""
	if len(bridges) > 0 {
		via = fmt.Sprintf(" via %s bridges", *bridge)
	}
	setDetail("Tor · Bootstrapped 0%")
	appendLog(dir, "tor", fmt.Sprintf("starting, region %s%s", orFastest(region), via))

	return runExitProcess(dir, "tor",
		[]string{torBinary, "-f", torrc},
		torDir,
		parseTorOutput,
		false, // Tor writes its own log (torrc)
		func() { followTorControl(dir, dataDir, setDetail) },
		onExit,
	)
}

// parseTorOutput: only what Tor prints before opening its log file (a bad torrc). Progress
// and ready come from the control port.
func parseTorOutput(line string) exitLine {
	if torErrRe.MatchString(line) {
		return exitLine{kind: lineError, message: lineAfterBracket(line)}
	}
	return exitLine{}
}

func lineAfterBracket(line string) string {
	if i := strings.Index(line, "] "); i >= 0 {
		return line[i+2:]
	}
	return line
}

// followTorControl is TorExit.control: bootstrap progress (the home page's detail line, and
// ready at 100%), and circuits lost / established once connected (reconnecting).
func followTorControl(dir, dataDir string, setDetail func(string)) {
	cookie := filepath.Join(dataDir, "control_auth_cookie")
	conn := openTorControl(cookie)
	if conn == nil {
		return
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	send := func(command string) {
		fmt.Fprintf(conn, "%s\r\n", command)
	}
	authCookie, err := os.ReadFile(cookie)
	if err != nil {
		appendLog(dir, "tor", "control port login failed: no cookie")
		return
	}
	send("AUTHENTICATE " + hex.EncodeToString(authCookie))
	if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, "250") {
		appendLog(dir, "tor", "control port login failed: "+strings.TrimSpace(line))
		return
	}
	send("SETEVENTS STATUS_CLIENT")
	if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, "250") {
		return
	}
	// The phase reached before we subscribed; events follow from here.
	send("GETINFO status/bootstrap-phase")

	connected := false
	lost := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return // Tor exited or was stopped
		}
		switch {
		case strings.Contains(line, " BOOTSTRAP "):
			if m := torProgressRe.FindStringSubmatch(line); m != nil {
				progress := 0
				fmt.Sscanf(m[1], "%d", &progress)
				if progress >= 100 {
					connected = true
					setDetail("")
				} else if !connected {
					setDetail(fmt.Sprintf("Tor · Bootstrapped %d%%", progress))
				}
			}
		case strings.Contains(line, "CIRCUIT_NOT_ESTABLISHED"):
			if connected && !lost {
				lost = true
				reason := ""
				if m := torReasonRe.FindStringSubmatch(line); m != nil {
					reason = " (" + m[1] + ")"
				}
				appendLog(dir, "tor", "circuits lost"+reason)
				setDetail("Tor · Reconnecting…")
			}
		case strings.Contains(line, "CIRCUIT_ESTABLISHED"):
			if lost {
				lost = false
				appendLog(dir, "tor", "circuits established")
				setDetail("")
			}
		}
	}
}

// openTorControl connects once Tor has written its cookie and opened the port; nil if Tor
// stops first.
func openTorControl(cookie string) net.Conn {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if st, err := os.Stat(cookie); err == nil && st.Size() == 32 {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", torControlPort), time.Second)
			if err == nil {
				return conn
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// builtInBridges is TorExit.builtInBridges: Tor's built-in bridges for a transport, from the
// bundled pt_config.json.
func builtInBridges(binDir, transport string) []string {
	raw, err := os.ReadFile(filepath.Join(binDir, "resources", "tor", "pt_config.json"))
	if err != nil {
		return nil
	}
	var config struct {
		Bridges map[string][]string `json:"bridges"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return nil
	}
	return config.Bridges[transport]
}

// snowflakeAmpBridge is TorExit.snowflakeAmpBridge: lyrebird takes no command-line flags, so
// the rendezvous is set with bridge-line arguments.
func snowflakeAmpBridge(bridge string) string {
	var kept []string
	for _, part := range strings.Fields(bridge) {
		key := part
		if i := strings.Index(part, "="); i > 0 {
			key = part[:i]
		}
		switch key {
		case "url", "front", "fronts", "ampcache":
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, " ") +
		" url=https://snowflake-broker.torproject.net/ ampcache=https://cdn.ampproject.org/ front=www.google.com"
}

// endregion

// region SetupTor (SetupTor.kt): the setup wizard's Tor, with HTTP CONNECT for Dart's client.

type setupTor struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	progress int
	err      string
}

// startSetupTor starts the wizard's Tor with the given bridges; all in one torrc (Tor uses
// whichever connects). Its own data directory and ports, so it never meets TorExit.
func (s *Service) startSetupTor(bridges []string) string {
	if len(bridges) == 0 {
		return "No bridges"
	}
	s.stopSetupTor()
	torDir := filepath.Join(s.dir, "tor-setup")
	dataDir := filepath.Join(torDir, "data")
	os.MkdirAll(dataDir, 0o755)
	torBinary := filepath.Join(s.binDir, "tor", "tor"+exeExt())
	lyrebird := filepath.Join(s.binDir, "tor", "lyrebird"+exeExt())

	lines := []string{
		"DataDirectory " + dataDir,
		"SocksPort 0",
		// Plain HTTP CONNECT, which Dart's HttpClient speaks (it has no SOCKS).
		fmt.Sprintf("HTTPTunnelPort 127.0.0.1:%d", setupTorHTTPPort),
		fmt.Sprintf("ControlPort 127.0.0.1:%d", setupTorControlPrt),
		"CookieAuthentication 1",
		"Log notice file " + filepath.Join(torDir, "tor.log"),
		"ClientOnly 1",
		"AvoidDiskWrites 1",
		"UseBridges 1",
		fmt.Sprintf("ClientTransportPlugin meek_lite,obfs4,webtunnel exec %s", lyrebird),
		fmt.Sprintf("ClientTransportPlugin snowflake exec %s", lyrebird),
	}
	for _, b := range bridges {
		lines = append(lines, "Bridge "+b)
	}
	torrc := filepath.Join(torDir, "torrc")
	if err := os.WriteFile(torrc, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err.Error()
	}
	os.Remove(filepath.Join(dataDir, "control_auth_cookie"))

	cmd := exec.Command(torBinary, "-f", torrc)
	cmd.Dir = torDir
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	st := &setupTor{cmd: cmd}
	s.mu.Lock()
	s.setupTor = st
	s.mu.Unlock()

	go copyLines(pr, func(line string) {
		logf("setup-tor: %s", line)
		if torErrRe.MatchString(line) {
			st.mu.Lock()
			st.err = lineAfterBracket(line)
			st.mu.Unlock()
		}
	})
	go func() {
		_ = cmd.Wait()
		pw.Close()
		st.mu.Lock()
		wasStopped := st.cmd == nil
		st.cmd = nil
		if st.err == "" && !wasStopped {
			st.err = "Tor stopped"
		}
		st.mu.Unlock()
	}()
	go followSetupTor(st, dataDir)
	return ""
}

// stopSetupTor stops the wizard's Tor, if it is running.
func (s *Service) stopSetupTor() {
	s.mu.Lock()
	st := s.setupTor
	s.mu.Unlock()
	if st != nil {
		st.stop()
	}
}

// followSetupTor reads bootstrap progress from the control port until it reaches 100 or Tor
// stops.
func followSetupTor(st *setupTor, dataDir string) {
	cookie := filepath.Join(dataDir, "control_auth_cookie")
	for {
		st.mu.Lock()
		running := st.cmd != nil
		st.mu.Unlock()
		if !running {
			return
		}
		if st_, err := os.Stat(cookie); err == nil && st_.Size() == 32 {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", setupTorControlPrt), time.Second)
			if err == nil {
				followSetupTorEvents(st, conn, cookie)
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func followSetupTorEvents(st *setupTor, conn net.Conn, cookie string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	send := func(command string) { fmt.Fprintf(conn, "%s\r\n", command) }
	authCookie, err := os.ReadFile(cookie)
	if err != nil {
		return
	}
	send("AUTHENTICATE " + hex.EncodeToString(authCookie))
	if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, "250") {
		return
	}
	send("SETEVENTS STATUS_CLIENT")
	if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, "250") {
		return
	}
	send("GETINFO status/bootstrap-phase")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if strings.Contains(line, " BOOTSTRAP ") {
			if m := torProgressRe.FindStringSubmatch(line); m != nil {
				progress := 0
				fmt.Sscanf(m[1], "%d", &progress)
				st.mu.Lock()
				if progress > st.progress {
					st.progress = progress
				}
				done := st.progress >= 100
				st.mu.Unlock()
				if done {
					return
				}
			}
		}
	}
}

func (st *setupTor) status() (int, bool, string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.progress, st.cmd != nil, st.err
}

func (st *setupTor) stop() {
	st.mu.Lock()
	cmd := st.cmd
	st.cmd = nil
	st.mu.Unlock()
	if cmd != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
}

// endregion

// orFastest formats an empty region for the log lines.
func orFastest(region string) string {
	if region == "" {
		return "fastest"
	}
	return region
}
