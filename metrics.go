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
	defaultPort    = "7000"
)

var (
	metrics       strings.Builder
	metricsMutex  sync.RWMutex
	pidCache      map[int32]*process.Process
	pidCacheMutex sync.RWMutex
	procMount     string
	sysMount      string
)

func init() {
	procMount = os.Getenv("PROC_MOUNT")
	if procMount == "" {
		procMount = "/proc"
	}
	sysMount = os.Getenv("SYS_MOUNT")
	if sysMount == "" {
		sysMount = "/sys"
	}
}

func main() {
	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = defaultPort
	}
	listenAddr := ":" + port

	pidCache = make(map[int32]*process.Process)
	go updateMetrics()
	http.HandleFunc("/metrics", serveMetrics)
	log.Printf("Starting metrics server on %s", listenAddr)
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
	pids, err := getActivePIDs()
	if err != nil {
		return "", fmt.Errorf("error getting active PIDs: %v", err)
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

func getActivePIDs() ([]int32, error) {
	files, err := ioutil.ReadDir(procMount)
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
	p, err := getOrCreateProcess(pid)
	if err != nil {
		return "", err
	}

	name, err := p.Name()
	if err != nil {
		return "", err
	}

	cmdline, err := p.Cmdline()
	if err != nil || cmdline == "" {
		cmdline = name
	}

	cmd := filepath.Base(strings.Fields(cmdline)[0])
	args := strings.Join(strings.Fields(cmdline)[1:], " ")

	cpu, err := p.CPUPercent()
	if err != nil {
		return "", err
	}

	mem, err := p.MemoryInfo()
	if err != nil {
		return "", err
	}

	ioCounters, err := p.IOCounters()
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
	fmt.Fprintf(&sb, "process_memory_usage{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, mem.RSS)
	fmt.Fprintf(&sb, "process_network_receive_bytes{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, recv)
	fmt.Fprintf(&sb, "process_network_transmit_bytes{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, trans)
	fmt.Fprintf(&sb, "process_disk_read_bytes{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, ioCounters.ReadBytes)
	fmt.Fprintf(&sb, "process_disk_write_bytes{pid=\"%d\",command=\"%s\",args=\"%s\"} %d\n", pid, cmd, args, ioCounters.WriteBytes)

	return sb.String(), nil
}

func getOrCreateProcess(pid int32) (*process.Process, error) {
	pidCacheMutex.RLock()
	p, exists := pidCache[pid]
	pidCacheMutex.RUnlock()

	if exists {
		return p, nil
	}

	p, err := process.NewProcess(pid)
	if err != nil {
		return nil, err
	}

	pidCacheMutex.Lock()
	pidCache[pid] = p
	pidCacheMutex.Unlock()

	return p, nil
}

func getNetworkIO(pid int32) (uint64, uint64, error) {
	content, err := os.ReadFile(fmt.Sprintf("%s/%d/net/dev", procMount, pid))
	if err != nil {
		return 0, 0, err
	}

	var recvBytes, transBytes uint64
	lines := strings.Split(string(content), "\n")
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
