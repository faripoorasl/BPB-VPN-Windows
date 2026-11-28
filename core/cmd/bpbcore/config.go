// The Windows TUN and per-app adaptation of the app's config (xray_config.dart builds the
// same JSON on every platform; Android's VPN service adds routes/DNS/per-app at the
// VpnService layer — this file is that layer for Windows).
package main

import (
	"encoding/json"
	"strings"
)

// The TUN network, mirroring BpbVpnService's builder: the same addresses, DNS server and
// routes. IPv6 is always captured so it cannot leak around the tunnel; whether IPv6 is
// actually used is the DNS queryStrategy's decision, as on Android.
const (
	tunIPv4   = "10.10.14.1/30"
	tunIPv6   = "fd66:b9b:10:14::1/126"
	tunDNS    = "10.10.14.2"
	tunDevice = "xray0"
)

// adaptConfig adds the Windows-only parts to the app's config:
//   - the TUN inbound's gateway, DNS server, system routes and WFP leak protection;
//   - the per-app rules, as process-based routing rules (Windows' equivalent of the
//     VpnService allow/disallow lists).
func adaptConfig(config string, opts *startOptions, dir string) (string, string) {
	var json_ map[string]any
	if err := json.Unmarshal([]byte(config), &json_); err != nil {
		return "", "config is not a JSON object: " + err.Error()
	}

	if !opts.ProxyOnly {
		if errText := applyTunSettings(json_, opts); errText != "" {
			return "", errText
		}
	}
	if opts.PerAppMode == "include" || opts.PerAppMode == "exclude" {
		applyPerAppRules(json_, opts)
	}
	data, err := json.Marshal(json_)
	if err != nil {
		return "", err.Error()
	}
	return string(data), ""
}

// applyTunSettings finds the tun inbound and adds what Windows needs to route the system's
// traffic into it (Android's VpnService.addRoute / addDnsServer equivalents).
func applyTunSettings(config map[string]any, opts *startOptions) string {
	inbounds, _ := config["inbounds"].([]any)
	var tun map[string]any
	for _, inbound := range inbounds {
		m, _ := inbound.(map[string]any)
		if m != nil && m["tag"] == "tun" && m["protocol"] == "tun" {
			tun = m
			break
		}
	}
	if tun == nil {
		return "The config has no tun inbound for VPN mode"
	}
	settings, _ := tun["settings"].(map[string]any)
	if settings == nil {
		settings = map[string]any{}
		tun["settings"] = settings
	}
	settings["name"] = tunDevice
	if opts.MTU > 0 {
		settings["MTU"] = opts.MTU
	}
	// The adapter's addresses and its DNS server, the same values the Android builder sets.
	settings["gateway"] = []string{tunIPv4, tunIPv6}
	settings["dns"] = []string{tunDNS}
	// The whole system's traffic into the TUN (addRoute("0.0.0.0", 0) + addRoute("::", 0)).
	// With routes set, the core also binds its own outbound sockets to the physical
	// interface (autoOutboundsInterface "auto"), which is Android's self-exclusion.
	settings["autoSystemRoutingTable"] = []string{"0.0.0.0/0", "::/0"}
	// Block the leaks the routes cannot: DNS client service bypasses and other misrouted
	// traffic (the Android TUN captures everything by construction).
	settings["autoSystemWfpBlockLeak"] = []string{"dns", "windnsclient", "misconfigtun"}
	return ""
}

// applyPerAppRules translates perAppMode/perAppApps into routing rules, right before the
// catch-all rule. The process matcher matches executable names, with or without ".exe"
// (Xray's ProcessNameMatcher strips the suffix).
//
//	exclude: the listed apps' traffic goes out directly, everything else is proxied —
//	         the direct outbound is bound to the physical interface, so it leaves the tunnel.
//	include: only the listed apps are proxied; everything else goes out directly.
func applyPerAppRules(config map[string]any, opts *startOptions) {
	routing, _ := config["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	if len(rules) == 0 {
		return
	}
	apps := make([]string, 0, len(opts.Apps))
	for _, app := range opts.Apps {
		if app = strings.TrimSpace(app); app != "" {
			apps = append(apps, strings.TrimSuffix(strings.ToLower(app), ".exe"))
		}
	}
	if len(apps) == 0 {
		return
	}

	// The catch-all rule is the last one (see XrayConfigBuilder._buildRoutingRules).
	catchAll, _ := rules[len(rules)-1].(map[string]any)
	if catchAll == nil {
		return
	}

	processRule := map[string]any{"type": "field", "process": apps}
	if opts.PerAppMode == "exclude" {
		// Listed apps bypass the proxy; the rest of the rules (block lists, DNS, the
		// catch-all proxy) keep applying to everything else.
		processRule["outboundTag"] = "direct"
		rules = append(rules[:len(rules)-1], processRule, catchAll)
	} else {
		// Only the listed apps are proxied: they take the catch-all's target (the proxy
		// outbound or the leastPing balancer), and the catch-all itself becomes direct.
		if tag, ok := catchAll["outboundTag"]; ok {
			processRule["outboundTag"] = tag
		} else if tag, ok := catchAll["balancerTag"]; ok {
			processRule["balancerTag"] = tag
		} else {
			return // unknown catch-all shape; leave the config alone
		}
		catchAll["outboundTag"] = "direct"
		delete(catchAll, "balancerTag")
		rules = append(rules[:len(rules)-1], processRule, catchAll)
	}
	routing["rules"] = rules
}
