// Per-second traffic, split into proxied and direct, sampled from Xray's outbound stats
// (TrafficMeter.kt). Xray resets its counters on every query, so this is the only reader.
package main

import (
	"strconv"
	"strings"
	"sync"
)

type trafficSample struct {
	ProxyUp    int64
	ProxyDown  int64
	DirectUp   int64
	DirectDown int64
}

func (a trafficSample) add(b trafficSample) trafficSample {
	return trafficSample{
		ProxyUp: a.ProxyUp + b.ProxyUp, ProxyDown: a.ProxyDown + b.ProxyDown,
		DirectUp: a.DirectUp + b.DirectUp, DirectDown: a.DirectDown + b.DirectDown,
	}
}

// TrafficMeter reads (and thereby resets) Xray's counters once per second while connected.
type TrafficMeter struct {
	mu    sync.Mutex
	rate  trafficSample
	total trafficSample
}

// ignoredTags are outbounds that are neither proxy nor direct traffic, and exits whose
// traffic is already counted on the outbound carrying it (TrafficMeter.IGNORED).
var ignoredTags = map[string]bool{"block": true, "dns-out": true, "exit": true}

// sample reads the counters through read (the controller's OutboundTraffic) and updates the
// rates and totals.
func (t *TrafficMeter) sample(read func() string) {
	raw := read()
	var sample trafficSample
	// "tag,uplink|downlink,bytes;..."
	for _, entry := range strings.Split(raw, ";") {
		parts := strings.Split(entry, ",")
		if len(parts) != 3 || ignoredTags[parts[0]] ||
			strings.HasPrefix(parts[0], "chain-") || strings.HasPrefix(parts[0], "redirect-out-") {
			continue
		}
		bytes, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || bytes <= 0 {
			continue
		}
		up := parts[1] == "uplink"
		if parts[0] == "direct" {
			if up {
				sample.DirectUp += bytes
			} else {
				sample.DirectDown += bytes
			}
		} else {
			if up {
				sample.ProxyUp += bytes
			} else {
				sample.ProxyDown += bytes
			}
		}
	}
	t.mu.Lock()
	t.rate = sample
	t.total = t.total.add(sample)
	t.mu.Unlock()
}

func (t *TrafficMeter) reset() {
	t.mu.Lock()
	t.rate = trafficSample{}
	t.total = trafficSample{}
	t.mu.Unlock()
}

func (t *TrafficMeter) snapshot() (rate, total trafficSample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rate, t.total
}
