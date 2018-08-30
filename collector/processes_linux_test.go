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
	"os"
	"testing"

	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

func TestReadProcessStatus(t *testing.T) {
	if _, err := kingpin.CommandLine.Parse([]string{"--path.procfs", "fixtures/proc"}); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open("fixtures/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	memInfo, err := parseMemInfo(file)
	if err != nil {
		t.Fatal(err)
	}
	totalMemory := memInfo["MemTotal_bytes"]

	totalCPUTime := float64(301854 + 111922)

	want := 1
	processStat, err := getAllocatedThreads(totalCPUTime, totalMemory)
	if err != nil {
		t.Fatalf("Cannot retrieve data from procfs getAllocatedThreads function: %v ", err)
	}

	if processStat.threads < want {
		t.Fatalf("Current threads: %d Shouldn't be less than wanted %d", processStat.threads, want)
	}

	if processStat.states == nil {
		t.Fatalf("Process states cannot be nil %v:", processStat.states)
	}

	if processStat.cpuUsage == nil {
		t.Fatalf("Process cpu usage cannot be nil %v:", processStat.cpuUsage)
	}

	if processStat.memoryUsage == nil {
		t.Fatalf("Process memory usage cannot be nil %v:", processStat.memoryUsage)
	}

	if processStat.fileDescriptors == nil {
		t.Fatalf("Process file descriptors cannot be nil %v:", processStat.fileDescriptors)
	}

	maxPid, err := readUintFromFile(procFilePath("sys/kernel/pid_max"))
	if err != nil {
		t.Fatalf("Unable to retrieve limit number of maximum pids alloved %v\n", err)
	}

	if uint64(processStat.pids) > maxPid || processStat.pids == 0 {
		t.Fatalf("Total running pids cannot be greater than %d or equals to 0", maxPid)
	}
}
