package agent

import (
	"testing"
	"time"
)

func TestParseNetworkCountersExcludesLoopbackAndMalformedRows(t *testing.T) {
	contents := `Inter-| Receive | Transmit
 lo: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0
 eth0: 1000 1 2 3 4 5 6 7 2000 9 10 11 12 13 14 15
 broken: not-a-number 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0`
	counters := parseNetworkCounters(contents)
	if len(counters) != 1 || counters["eth0"].receive != 1000 || counters["eth0"].transmit != 2000 {
		t.Fatalf("unexpected counters: %#v", counters)
	}
}

func TestNetworkRatesHandleNewInterfacesAndCounterReset(t *testing.T) {
	previous := map[string]networkCounters{
		"eth0": {receive: 1000, transmit: 2000},
		"wg0":  {receive: 500, transmit: 500},
	}
	current := map[string]networkCounters{
		"eth0": {receive: 3000, transmit: 5000},
		"wg0":  {receive: 10, transmit: 20},
		"eth1": {receive: 1_000_000, transmit: 1_000_000},
	}
	receive, transmit := networkRates(previous, current, 2*time.Second)
	if receive != 1000 || transmit != 1500 {
		t.Fatalf("unexpected rates receive=%d transmit=%d", receive, transmit)
	}
}

func TestDiskUsagePercent(t *testing.T) {
	if got := diskUsagePercent(1000, 250); got != 75 {
		t.Fatalf("disk usage=%v, want 75", got)
	}
	if got := diskUsagePercent(0, 0); got != 0 {
		t.Fatalf("zero-sized filesystem usage=%v", got)
	}
}
