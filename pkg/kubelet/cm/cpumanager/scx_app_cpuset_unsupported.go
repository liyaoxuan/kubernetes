//go:build !linux
// +build !linux

/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cpumanager

import "fmt"

type unsupportedBPFAppCPUSetOps struct{}

func newBPFAppCPUSetOps() bpfAppCPUSetOps {
	return unsupportedBPFAppCPUSetOps{}
}

func (unsupportedBPFAppCPUSetOps) ObjGet(path string) (int, error) {
	return 0, fmt.Errorf("sched_ext app cpuset BPF map sync is only supported on linux")
}

func (unsupportedBPFAppCPUSetOps) LookupElem(fd int, key *scxAppKey, value *scxAppCPUSet) error {
	return fmt.Errorf("sched_ext app cpuset BPF map sync is only supported on linux")
}

func (unsupportedBPFAppCPUSetOps) UpdateElem(fd int, key *scxAppKey, value *scxAppCPUSet) error {
	return fmt.Errorf("sched_ext app cpuset BPF map sync is only supported on linux")
}

func (unsupportedBPFAppCPUSetOps) Close(fd int) error {
	return nil
}
