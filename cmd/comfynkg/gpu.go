package main

import (
	"regexp"
	"strconv"
	"strings"

	"comfynkg/internal/run"
)

// gpuMemory is the VRAM each process uses, by PID, as nvidia-smi reports it (what the GPU really
// holds: models, CUDA context, memory PyTorch doesn't manage). nil without nvidia-smi.
func gpuMemory() map[int]float64 {
	out, err := run.Output(run.Command(nil, "nvidia-smi", "--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits"))
	if err != nil {
		return nil
	}
	return parseApps(out)
}

// parseApps reads "pid, MiB" lines; a process on several GPUs is summed.
func parseApps(out string) map[int]float64 {
	m := map[int]float64{}
	for _, l := range strings.Split(out, "\n") {
		pid, mib, ok := strings.Cut(l, ",")
		if !ok {
			continue
		}
		p, err1 := strconv.Atoi(strings.TrimSpace(pid))
		v, err2 := strconv.ParseFloat(strings.TrimSpace(mib), 64)
		if err1 == nil && err2 == nil {
			m[p] += v * 1024 * 1024
		}
	}
	return m
}

var driverRe = regexp.MustCompile(`Driver Version: (\S+)\s+CUDA Version: (\S+)`)

// driverVersions reads the NVIDIA driver and the CUDA version it supports from nvidia-smi's banner.
// Empty without nvidia-smi.
func driverVersions() (driver, cuda string) {
	out, err := run.Output(run.Command(nil, "nvidia-smi"))
	if err != nil {
		return "", ""
	}
	return parseDriver(out)
}

func parseDriver(out string) (driver, cuda string) {
	if m := driverRe.FindStringSubmatch(out); m != nil {
		return m[1], m[2]
	}
	return "", ""
}
