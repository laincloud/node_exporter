// Copyright 2018 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build !noprocesses

package collector

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"github.com/prometheus/procfs"
)

type (
	processCollector struct {
		threadAlloc               *prometheus.Desc
		threadLimit               *prometheus.Desc
		procsState                *prometheus.Desc
		procsCPUUsage             *prometheus.Desc
		procsMemoryUsage          *prometheus.Desc
		procsFileDescriptorOpened *prometheus.Desc
		pidUsed                   *prometheus.Desc
		pidMax                    *prometheus.Desc
	}

	processStatistics struct {
		threads         int
		states          map[string]int32
		cpuUsage        map[string]float64
		memoryUsage     map[string]float64
		fileDescriptors map[string]int
		pids            int
	}
)

const (
	cpuUsageMinThreshold        = 30.0 // min threshold of cpu usage, 30.0 is 30.0%
	memoryUsageMinThreshold     = 30.0 // min threshold of memory usage, 30.0 is 30.0%
	fileDescriptorsMinThreshold = 100  // min threshold of number of opened file descriptors, 100 is one hundred opened file descriptors
)

func init() {
	registerCollector("processes", defaultEnabled, NewProcessStatCollector)
}

// NewProcessStatCollector returns a new Collector exposing all processes statistics.
func NewProcessStatCollector() (Collector, error) {
	subsystem := "processes"
	return &processCollector{
		threadAlloc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "threads"),
			"Allocated threads in system",
			nil, nil,
		),
		threadLimit: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "max_threads"),
			"Limit of threads in the system",
			nil, nil,
		),
		procsState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "state"),
			"Number of processes in each state.",
			[]string{"state"}, nil,
		),
		procsCPUUsage: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "cpu_usage"),
			"CPU usage of a process.",
			[]string{"command_cpu_usage"}, nil,
		),
		procsMemoryUsage: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "memory_usage"),
			"Memory usage of a process.",
			[]string{"command_memory_usage"}, nil,
		),
		procsFileDescriptorOpened: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "file_descriptors"),
			"Total opened file descriptors of a process.",
			[]string{"command_file_descriptors"}, nil,
		),
		pidUsed: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "pids"),
			"Number of PIDs",
			nil, nil,
		),
		pidMax: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "max_processes"),
			"Number of max PIDs limit",
			nil, nil,
		),
	}, nil
}

func (t *processCollector) Update(ch chan<- prometheus.Metric) error {
	stat, err := procfs.NewStat()
	if err != nil {
		return fmt.Errorf("unable to retrieve kernel/system statistics read from /proc/stat: %q", err)
	}

	totalCPUTime := stat.CPUTotal.User + stat.CPUTotal.System

	m := &meminfoCollector{}
	meminfo, err := m.getMemInfo()
	if err != nil {
		return fmt.Errorf("unable to retrieve memory statistics read from /proc/meminfo: %q", err)
	}
	totalMemory := meminfo["MemTotal_bytes"]

	processStat, err := getAllocatedThreads(totalCPUTime, totalMemory)
	if err != nil {
		return fmt.Errorf("unable to retrieve number of allocated threads: %q", err)
	}

	ch <- prometheus.MustNewConstMetric(t.threadAlloc, prometheus.GaugeValue, float64(processStat.threads))

	maxThreads, err := readUintFromFile(procFilePath("sys/kernel/threads-max"))
	if err != nil {
		return fmt.Errorf("unable to retrieve limit number of threads: %q", err)
	}
	ch <- prometheus.MustNewConstMetric(t.threadLimit, prometheus.GaugeValue, float64(maxThreads))

	for state := range processStat.states {
		ch <- prometheus.MustNewConstMetric(t.procsState, prometheus.GaugeValue, float64(processStat.states[state]), state)
	}

	for cpuUsage := range processStat.cpuUsage {
		ch <- prometheus.MustNewConstMetric(t.procsCPUUsage, prometheus.GaugeValue, processStat.cpuUsage[cpuUsage], cpuUsage)
	}

	for memoryUsage := range processStat.memoryUsage {
		ch <- prometheus.MustNewConstMetric(t.procsMemoryUsage, prometheus.GaugeValue, processStat.memoryUsage[memoryUsage], memoryUsage)
	}

	for fd := range processStat.fileDescriptors {
		ch <- prometheus.MustNewConstMetric(t.procsFileDescriptorOpened, prometheus.GaugeValue, float64(processStat.fileDescriptors[fd]), fd)
	}

	ch <- prometheus.MustNewConstMetric(t.pidUsed, prometheus.GaugeValue, float64(processStat.pids))

	pidM, err := readUintFromFile(procFilePath("sys/kernel/pid_max"))
	if err != nil {
		return fmt.Errorf("unable to retrieve limit number of maximum pids alloved: %q", err)
	}
	ch <- prometheus.MustNewConstMetric(t.pidMax, prometheus.GaugeValue, float64(pidM))

	return nil
}

func getAllocatedThreads(totalCPUTime, totalMemory float64) (*processStatistics, error) {
	fs, err := procfs.NewFS(*procPath)
	if err != nil {
		return nil, err
	}

	procs, err := fs.AllProcs()
	if err != nil {
		return nil, err
	}

	pids := 0
	threads := 0
	procStates := make(map[string]int32)             // procStates's example: {sleeping: 15, running: 3}
	procCPUUsage := make(map[string]float64)         // procCPUUsage's example: {java-app: 3.1, golang-app: 9.8}
	procMemoryUsage := make(map[string]float64)      // procMemoryUsage's example: {java-app: 1.5, golang-app: 5.2}
	procFileDescriptorOpened := make(map[string]int) // procFileDescriptorOpened's example: {java-app: 3, golang-app: 14}
	for _, p := range procs {
		stat, err := p.NewStat()
		// PIDs can vanish between getting the list and getting stats.
		if os.IsNotExist(err) {
			log.Debugf("file not found when retrieving stats: %q", err)
			continue
		}
		if err != nil {
			return nil, err
		}

		pids += 1
		threads += stat.NumThreads
		procStates[stat.State] += 1

		cmdLine, err := p.CmdLine()
		if err != nil {
			return nil, err
		}

		cpuTime := stat.CPUTime()
		cpuTimeUsage, err := strconv.ParseFloat(fmt.Sprintf("%.1f", cpuTime/totalCPUTime*100), 64)
		if err != nil {
			return nil, err
		}
		if cpuTimeUsage >= cpuUsageMinThreshold {
			procCPUUsage[strings.Join([]string{strings.Join(cmdLine, "_"), "cpu_usage"}, "_")] = cpuTimeUsage
		}

		memory := float64(stat.RSS * 4 * 1024)
		memoryUsage, err := strconv.ParseFloat(fmt.Sprintf("%.1f", memory/totalMemory*100), 64)
		if err != nil {
			return nil, err
		}
		if memoryUsage >= memoryUsageMinThreshold {
			procMemoryUsage[strings.Join([]string{strings.Join(cmdLine, "_"), "memory_usage"}, "_")] = memoryUsage
		}

		fds, err := p.FileDescriptorsLen()
		if err != nil {
			return nil, err
		}
		if fds >= fileDescriptorsMinThreshold {
			procFileDescriptorOpened[strings.Join([]string{strings.Join(cmdLine, "_"), "fds"}, "_")] = fds
		}
	}

	return &processStatistics{
		threads:         threads,
		states:          procStates,
		cpuUsage:        procCPUUsage,
		memoryUsage:     procMemoryUsage,
		fileDescriptors: procFileDescriptorOpened,
		pids:            pids,
	}, nil
}
