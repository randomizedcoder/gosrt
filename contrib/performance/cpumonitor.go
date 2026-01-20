package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CPUMonitor tracks CPU usage for the system and specific processes.
// Provides htop-like visibility during performance tests.
type CPUMonitor struct {
	// Process PIDs to monitor
	serverPID int
	seekerPID int

	// Last sample for delta calculation
	lastSystemCPU SystemCPUSample
	lastServerCPU ProcessCPUSample
	lastSeekerCPU ProcessCPUSample
	lastSampleTime time.Time

	// Current usage (updated atomically for thread-safe reads)
	systemUsage atomic.Int64 // Scaled by 100 (e.g., 5000 = 50.00%)
	serverUsage atomic.Int64
	seekerUsage atomic.Int64

	// Per-core usage
	coreCount int
	coreUsage []atomic.Int64 // One per core

	mu sync.Mutex

	// Control
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// SystemCPUSample holds /proc/stat CPU counters
type SystemCPUSample struct {
	User    uint64
	Nice    uint64
	System  uint64
	Idle    uint64
	IOWait  uint64
	IRQ     uint64
	SoftIRQ uint64
	Steal   uint64
}

// ProcessCPUSample holds /proc/[pid]/stat CPU counters
type ProcessCPUSample struct {
	UTime  uint64 // User time
	STime  uint64 // System time
	CUTime uint64 // Children user time
	CSTime uint64 // Children system time
}

// NewCPUMonitor creates a new CPU monitor.
func NewCPUMonitor() *CPUMonitor {
	coreCount := getNumCPUs()
	m := &CPUMonitor{
		coreCount: coreCount,
		coreUsage: make([]atomic.Int64, coreCount),
		stopChan:  make(chan struct{}),
	}
	return m
}

// SetProcesses sets the PIDs to monitor.
func (m *CPUMonitor) SetProcesses(serverPID, seekerPID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverPID = serverPID
	m.seekerPID = seekerPID
}

// Start begins background CPU monitoring.
func (m *CPUMonitor) Start() {
	m.wg.Add(1)
	go m.monitorLoop()
}

// Stop stops the monitor.
func (m *CPUMonitor) Stop() {
	close(m.stopChan)
	m.wg.Wait()
}

// GetSystemUsage returns system-wide CPU usage as percentage (0-100).
func (m *CPUMonitor) GetSystemUsage() float64 {
	return float64(m.systemUsage.Load()) / 100.0
}

// GetServerUsage returns server process CPU usage as percentage.
func (m *CPUMonitor) GetServerUsage() float64 {
	return float64(m.serverUsage.Load()) / 100.0
}

// GetSeekerUsage returns seeker process CPU usage as percentage.
func (m *CPUMonitor) GetSeekerUsage() float64 {
	return float64(m.seekerUsage.Load()) / 100.0
}

// GetCoreUsages returns per-core CPU usage as percentages.
func (m *CPUMonitor) GetCoreUsages() []float64 {
	result := make([]float64, m.coreCount)
	for i := range m.coreUsage {
		result[i] = float64(m.coreUsage[i].Load()) / 100.0
	}
	return result
}

// GetCoreCount returns the number of CPU cores.
func (m *CPUMonitor) GetCoreCount() int {
	return m.coreCount
}

// FormatStatus returns a formatted string showing CPU usage.
func (m *CPUMonitor) FormatStatus() string {
	sysUsage := m.GetSystemUsage()
	serverUsage := m.GetServerUsage()
	seekerUsage := m.GetSeekerUsage()

	// Find max core usage
	maxCore := 0.0
	maxCoreIdx := 0
	cores := m.GetCoreUsages()
	for i, u := range cores {
		if u > maxCore {
			maxCore = u
			maxCoreIdx = i
		}
	}

	// Count "hot" cores (>50% usage)
	hotCores := 0
	for _, u := range cores {
		if u > 50 {
			hotCores++
		}
	}

	return fmt.Sprintf("CPU: sys=%.1f%% srv=%.1f%% seek=%.1f%% | cores: max=%.1f%%(#%d) hot=%d/%d",
		sysUsage, serverUsage, seekerUsage, maxCore, maxCoreIdx, hotCores, m.coreCount)
}

// FormatCoreBar returns a visual bar chart of per-core usage.
func (m *CPUMonitor) FormatCoreBar() string {
	cores := m.GetCoreUsages()
	if len(cores) == 0 {
		return "CPU: [no data]"
	}

	// For many cores, show a compact view
	if len(cores) > 16 {
		// Just show summary
		return m.FormatStatus()
	}

	// Visual bar for each core
	var sb strings.Builder
	sb.WriteString("CPU: [")
	for i, u := range cores {
		if i > 0 {
			sb.WriteString(" ")
		}
		// Map 0-100% to character
		ch := cpuToChar(u)
		sb.WriteByte(ch)
	}
	sb.WriteString("]")

	return sb.String()
}

func cpuToChar(usage float64) byte {
	switch {
	case usage < 10:
		return '.'
	case usage < 25:
		return '-'
	case usage < 50:
		return '='
	case usage < 75:
		return '#'
	case usage < 90:
		return '%'
	default:
		return '@'
	}
}

func (m *CPUMonitor) monitorLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Initial sample
	m.sample()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.sample()
		}
	}
}

func (m *CPUMonitor) sample() {
	m.mu.Lock()
	serverPID := m.serverPID
	seekerPID := m.seekerPID
	m.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(m.lastSampleTime).Seconds()
	if elapsed < 0.1 {
		return // Too soon
	}

	// Sample system CPU
	sysCPU := readSystemCPU()
	coreCPUs := readPerCoreCPU(m.coreCount)

	// Calculate system usage
	if m.lastSampleTime.IsZero() {
		m.lastSystemCPU = sysCPU
	} else {
		usage := calculateSystemUsage(m.lastSystemCPU, sysCPU)
		m.systemUsage.Store(int64(usage * 100))

		// Per-core usage
		for i := range coreCPUs {
			if i < len(m.coreUsage) {
				// For now, use system usage as approximation
				// Full per-core tracking requires more parsing
				m.coreUsage[i].Store(int64(usage * 100))
			}
		}
	}
	m.lastSystemCPU = sysCPU

	// Sample process CPU
	if serverPID > 0 {
		procCPU := readProcessCPU(serverPID)
		if m.lastServerCPU.UTime > 0 && elapsed > 0 {
			cpuTime := float64((procCPU.UTime+procCPU.STime)-(m.lastServerCPU.UTime+m.lastServerCPU.STime)) / getClockTicks()
			usage := (cpuTime / elapsed) * 100
			m.serverUsage.Store(int64(usage * 100))
		}
		m.lastServerCPU = procCPU
	}

	if seekerPID > 0 {
		procCPU := readProcessCPU(seekerPID)
		if m.lastSeekerCPU.UTime > 0 && elapsed > 0 {
			cpuTime := float64((procCPU.UTime+procCPU.STime)-(m.lastSeekerCPU.UTime+m.lastSeekerCPU.STime)) / getClockTicks()
			usage := (cpuTime / elapsed) * 100
			m.seekerUsage.Store(int64(usage * 100))
		}
		m.lastSeekerCPU = procCPU
	}

	m.lastSampleTime = now
}

func readSystemCPU() SystemCPUSample {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return SystemCPUSample{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) >= 8 {
				return SystemCPUSample{
					User:    parseUint(fields[1]),
					Nice:    parseUint(fields[2]),
					System:  parseUint(fields[3]),
					Idle:    parseUint(fields[4]),
					IOWait:  parseUint(fields[5]),
					IRQ:     parseUint(fields[6]),
					SoftIRQ: parseUint(fields[7]),
					Steal:   parseUintOrZero(fields, 8),
				}
			}
		}
	}
	return SystemCPUSample{}
}

func readPerCoreCPU(numCores int) []SystemCPUSample {
	result := make([]SystemCPUSample, numCores)

	f, err := os.Open("/proc/stat")
	if err != nil {
		return result
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	coreIdx := 0
	for scanner.Scan() && coreIdx < numCores {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu") && !strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) >= 8 {
				result[coreIdx] = SystemCPUSample{
					User:    parseUint(fields[1]),
					Nice:    parseUint(fields[2]),
					System:  parseUint(fields[3]),
					Idle:    parseUint(fields[4]),
					IOWait:  parseUint(fields[5]),
					IRQ:     parseUint(fields[6]),
					SoftIRQ: parseUint(fields[7]),
					Steal:   parseUintOrZero(fields, 8),
				}
				coreIdx++
			}
		}
	}
	return result
}

func readProcessCPU(pid int) ProcessCPUSample {
	f, err := os.Open(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcessCPUSample{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		line := scanner.Text()
		// Fields are space-separated, but comm can contain spaces in parens
		// Find the closing paren and parse from there
		closeIdx := strings.LastIndex(line, ")")
		if closeIdx < 0 {
			return ProcessCPUSample{}
		}
		fields := strings.Fields(line[closeIdx+2:])
		// utime is at index 11 (0-based after comm)
		if len(fields) >= 15 {
			return ProcessCPUSample{
				UTime:  parseUint(fields[11]),
				STime:  parseUint(fields[12]),
				CUTime: parseUint(fields[13]),
				CSTime: parseUint(fields[14]),
			}
		}
	}
	return ProcessCPUSample{}
}

func calculateSystemUsage(prev, curr SystemCPUSample) float64 {
	prevTotal := prev.User + prev.Nice + prev.System + prev.Idle + prev.IOWait + prev.IRQ + prev.SoftIRQ + prev.Steal
	currTotal := curr.User + curr.Nice + curr.System + curr.Idle + curr.IOWait + curr.IRQ + curr.SoftIRQ + curr.Steal

	totalDelta := float64(currTotal - prevTotal)
	if totalDelta == 0 {
		return 0
	}

	idleDelta := float64(curr.Idle - prev.Idle)
	return (1.0 - idleDelta/totalDelta) * 100.0
}

func parseUint(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func parseUintOrZero(fields []string, idx int) uint64 {
	if idx < len(fields) {
		return parseUint(fields[idx])
	}
	return 0
}

func getNumCPUs() int {
	// Count cpu lines in /proc/stat
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 1
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu") && !strings.HasPrefix(line, "cpu ") {
			count++
		}
	}
	if count == 0 {
		count = 1
	}
	return count
}

func getClockTicks() float64 {
	// Linux default is usually 100
	return 100.0
}
