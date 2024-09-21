package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

const (
	updateInterval = 15 * time.Second
	listenAddr     = ":7000"
	hostProcPath   = "/host/proc"
	hostSysPath    = "/host/sys"
)

var (
	metrics       strings.Builder
	metricsMutex  sync.RWMutex
	pidCache      map[int32]*process.Process
	pidCacheMutex sync.RWMutex
)

func main() {
	pidCache = make(map[int32]*process.Process)
	go updateMetrics()
	http.HandleFunc("/metrics", serveMetrics)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func serveMetrics(w http.ResponseWriter, r *http.Request) {
	metricsMutex.RLock()
	defer metricsMutex.RUnlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(metrics.String()))
}

func updateMetrics() {
	for {
		newMetrics, err := collectMetrics()
		if err != nil {
			log.Printf("Error collecting metrics: %v", err)
		} else {
			metricsMutex.Lock()
			metrics.Reset()
			metrics.WriteString(newMetrics)
			metricsMutex.Unlock()
		}
		time.Sleep(updateInterval)
	}
}

func collectMetrics() (string, error) {
	pids, err := getHostPIDs()
	if err != nil {
		return "", fmt.Errorf("error getting host PIDs: %v", err)
	}

	var sb strings.Builder
	sb.Grow(len(pids) * 500) // Pre-allocate buffer

	var wg sync.WaitGroup
	results := make(chan string, len(pids))

	for _, pid := range pids {
		wg.Add(1)
		go func(pid int32) {
			defer wg.Done()
			if result, err := collectProcessMetrics(pid); err == nil {
				results <- result
			}
		}(pid)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		sb.WriteString(result)
	}

	return sb.String(), nil
}

func getHostPIDs() ([]int32, error) {
	files, err := ioutil.ReadDir(hostProcPath)
	if err != nil {
		return nil, err
	}

	pids := make([]int32, 0, len(files))
	for _, file := range files {
		if file.IsDir() {
			if pid, err := strconv.ParseInt(file.Name(), 10, 32); err == nil {
				pids = append(pids, int32(pid))
			}
		}
	}
	return pids, nil
}

func collectProcessMetrics(pid int32) (string, error) {
	cmdline, err := readHostProcFile(pid, "cmdline")
	if err != nil {
		return "", err
	}

	cmd, args := parseCommand(cmdline)

	cpu, err := readHostProcStat(pid)
	if err != nil {
		return "", err
	}

	mem, err := readHostProcStatus(pid)
	if err != nil {
		return "", err
	}

	ioCounters, err := readHostProcIO(pid)
	if err != nil {
		return "", err
	}

	recv, trans, err := getNetworkIO(pid)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.Grow(500)

	fmt.Fprintf(&sb, "process_cpu_usage{pid=\"%d\",command=\"%s\",args=\"%s\"} %.2f\n", pid, cmd, args, cpu)
	fmt.Fprintf(&sb, "process_memory_usage{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, mem)
	fmt.Fprintf(&sb, "process_network_receive_bytes{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, recv)
	fmt.Fprintf(&sb, "process_network_transmit_bytes{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, trans)
	fmt.Fprintf(&sb, "process_disk_read_bytes{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, ioCounters.ReadBytes)
	fmt.Fprintf(&sb, "process_disk_write_bytes{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, ioCounters.WriteBytes)

	return sb.String(), nil
}

func readHostProcFile(pid int32, file string) (string, error) {
	content, err := ioutil.ReadFile(fmt.Sprintf("%s/%d/%s", hostProcPath, pid, file))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

func parseCommand(cmdline string) (string, string) {
	parts := strings.Split(cmdline, "\x00")
	if len(parts) == 0 {
		return "", ""
	}
	cmd := filepath.Base(parts[0])
	args := strings.Join(parts[1:], " ")
	return cmd, args
}

func readHostProcStat(pid int32) (float64, error) {
	content, err := readHostProcFile(pid, "stat")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(content)
	if len(fields) < 14 {
		return 0, fmt.Errorf("invalid stat file for pid %d", pid)
	}
	utime, _ := strconv.ParseFloat(fields[13], 64)
	stime, _ := strconv.ParseFloat(fields[14], 64)
	return (utime + stime) / 100.0, nil
}

func readHostProcStatus(pid int32) (uint64, error) {
	content, err := readHostProcFile(pid, "status")
	if err != nil {
		return 0, err
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				rss, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return rss * 1024, nil // Convert from KB to bytes
			}
		}
	}
	return 0, fmt.Errorf("VmRSS not found in status for pid %d", pid)
}

func readHostProcIO(pid int32) (*process.IOCountersStat, error) {
	content, err := readHostProcFile(pid, "io")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(content, "\n")
	counters := &process.IOCountersStat{}
	for _, line := range lines {
		if strings.HasPrefix(line, "read_bytes:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				counters.ReadBytes, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		} else if strings.HasPrefix(line, "write_bytes:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				counters.WriteBytes, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
	}
	return counters, nil
}

func getNetworkIO(pid int32) (uint64, uint64, error) {
	content, err := readHostProcFile(pid, "net/dev")
	if err != nil {
		return 0, 0, err
	}

	var recvBytes, transBytes uint64
	lines := strings.Split(content, "\n")
	for i := 2; i < len(lines); i++ {
		fields := strings.Fields(lines[i])
		if len(fields) < 17 {
			continue
		}
		if rb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
			recvBytes += rb
		}
		if tb, err := strconv.ParseUint(fields[9], 10, 64); err == nil {
			transBytes += tb
		}
	}

	return recvBytes, transBytes, nil
}
