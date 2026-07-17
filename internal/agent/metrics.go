package agent

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type SystemMetrics struct {
	CPUPercent    float64
	MemoryPercent float64
	LoadOne       float64
	DiskPercent   float64
	NetworkRxBps  uint64
	NetworkTxBps  uint64
}

type MetricsCollector struct {
	previousTotal     uint64
	previousIdle      uint64
	previousNetwork   map[string]networkCounters
	previousNetworkAt time.Time
}

type networkCounters struct{ receive, transmit uint64 }

func (collector *MetricsCollector) Sample() SystemMetrics {
	metrics := SystemMetrics{}
	if contents, err := os.ReadFile("/proc/stat"); err == nil {
		line := strings.SplitN(string(contents), "\n", 2)[0]
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "cpu" {
			var total uint64
			var values []uint64
			for _, field := range fields[1:] {
				value, err := strconv.ParseUint(field, 10, 64)
				if err != nil {
					break
				}
				values = append(values, value)
				total += value
			}
			if len(values) >= 4 && collector.previousTotal > 0 && total > collector.previousTotal {
				idle := values[3]
				if len(values) > 4 {
					idle += values[4]
				}
				deltaTotal := total - collector.previousTotal
				deltaIdle := idle - collector.previousIdle
				if deltaIdle <= deltaTotal {
					metrics.CPUPercent = float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
				}
				collector.previousIdle = idle
			} else if len(values) >= 4 {
				collector.previousIdle = values[3]
				if len(values) > 4 {
					collector.previousIdle += values[4]
				}
			}
			collector.previousTotal = total
		}
	}
	if contents, err := os.ReadFile("/proc/meminfo"); err == nil {
		values := map[string]uint64{}
		for _, line := range strings.Split(string(contents), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				value, err := strconv.ParseUint(fields[1], 10, 64)
				if err == nil {
					values[strings.TrimSuffix(fields[0], ":")] = value
				}
			}
		}
		total, available := values["MemTotal"], values["MemAvailable"]
		if total > 0 && available <= total {
			metrics.MemoryPercent = float64(total-available) / float64(total) * 100
		}
	}
	if contents, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(contents))
		if len(fields) > 0 {
			metrics.LoadOne, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs("/", &filesystem); err == nil {
		metrics.DiskPercent = diskUsagePercent(filesystem.Blocks, filesystem.Bfree)
	}
	if contents, err := os.ReadFile("/proc/net/dev"); err == nil {
		now := time.Now()
		current := parseNetworkCounters(string(contents))
		if len(collector.previousNetwork) > 0 && !collector.previousNetworkAt.IsZero() {
			metrics.NetworkRxBps, metrics.NetworkTxBps = networkRates(collector.previousNetwork, current, now.Sub(collector.previousNetworkAt))
		}
		collector.previousNetwork = current
		collector.previousNetworkAt = now
	}
	return metrics
}

func diskUsagePercent(blocks, free uint64) float64 {
	if blocks == 0 || free > blocks {
		return 0
	}
	return float64(blocks-free) / float64(blocks) * 100
}

func parseNetworkCounters(contents string) map[string]networkCounters {
	result := make(map[string]networkCounters)
	for _, line := range strings.Split(contents, "\n") {
		nameAndValues := strings.SplitN(line, ":", 2)
		if len(nameAndValues) != 2 {
			continue
		}
		name := strings.TrimSpace(nameAndValues[0])
		fields := strings.Fields(nameAndValues[1])
		if name == "" || name == "lo" || len(fields) < 9 {
			continue
		}
		receive, receiveErr := strconv.ParseUint(fields[0], 10, 64)
		transmit, transmitErr := strconv.ParseUint(fields[8], 10, 64)
		if receiveErr == nil && transmitErr == nil {
			result[name] = networkCounters{receive: receive, transmit: transmit}
		}
	}
	return result
}

func networkRates(previous, current map[string]networkCounters, elapsed time.Duration) (uint64, uint64) {
	if elapsed <= 0 {
		return 0, 0
	}
	var receiveBytes, transmitBytes uint64
	for name, counters := range current {
		prior, exists := previous[name]
		if !exists {
			continue
		}
		if counters.receive >= prior.receive {
			receiveBytes += counters.receive - prior.receive
		}
		if counters.transmit >= prior.transmit {
			transmitBytes += counters.transmit - prior.transmit
		}
	}
	seconds := elapsed.Seconds()
	return uint64(float64(receiveBytes) / seconds), uint64(float64(transmitBytes) / seconds)
}
