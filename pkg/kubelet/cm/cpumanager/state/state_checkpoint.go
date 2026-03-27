/*
Copyright 2018 The Kubernetes Authors.

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

package state

import (
	"fmt"
	"path/filepath"
	"sync"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
	"k8s.io/kubernetes/pkg/kubelet/cm/containermap"
	"k8s.io/utils/cpuset"
)

var _ State = &stateCheckpoint{}

type stateCheckpoint struct {
	mux               sync.RWMutex
	policyName        string
	cache             State
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
	initialContainers containermap.ContainerMap
}

// NewCheckpointState creates new State for keeping track of cpu/pod assignment with checkpoint backend
func NewCheckpointState(stateDir, checkpointName, policyName string, initialContainers containermap.ContainerMap) (State, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}
	stateCheckpoint := &stateCheckpoint{
		cache:             NewMemoryState(),
		policyName:        policyName,
		checkpointManager: checkpointManager,
		checkpointName:    checkpointName,
		initialContainers: initialContainers,
	}

	if err := stateCheckpoint.restoreState(); err != nil {
		//nolint:staticcheck // ST1005 user-facing error message
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete the CPU manager checkpoint file %q before restarting Kubelet",
			err, filepath.Join(stateDir, checkpointName))
	}

	return stateCheckpoint, nil
}

// migrateV1CheckpointToV2Checkpoint() converts checkpoints from the v1 format to the v2 format
func (sc *stateCheckpoint) migrateV1CheckpointToV2Checkpoint(src *CPUManagerCheckpointV1, dst *CPUManagerCheckpointV2) error {
	if src.PolicyName != "" {
		dst.PolicyName = src.PolicyName
	}
	if src.DefaultCPUSet != "" {
		dst.DefaultCPUSet = src.DefaultCPUSet
	}
	for containerID, cset := range src.Entries {
		podUID, containerName, err := sc.initialContainers.GetContainerRef(containerID)
		if err != nil {
			return fmt.Errorf("containerID '%v' not found in initial containers list", containerID)
		}
		if dst.Entries == nil {
			dst.Entries = make(map[string]map[string]string)
		}
		if _, exists := dst.Entries[podUID]; !exists {
			dst.Entries[podUID] = make(map[string]string)
		}
		dst.Entries[podUID][containerName] = cset
	}
	return nil
}

func (sc *stateCheckpoint) loadCheckpointV3() (*CPUManagerCheckpointV3, error) {
	checkpointV3 := newCPUManagerCheckpointV3()
	err := sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpointV3)
	if err != nil {
		return nil, err
	}
	return checkpointV3, nil
}

func (sc *stateCheckpoint) loadAndMigrateCheckpointV2() (*CPUManagerCheckpointV2, error) {
	checkpointV1 := newCPUManagerCheckpointV1()
	checkpointV2 := newCPUManagerCheckpointV2()

	err := sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpointV1)
	if err != nil {
		checkpointV1 = &CPUManagerCheckpointV1{}
		checkpointV2 = newCPUManagerCheckpointV2()
		if err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpointV2); err != nil {
			return nil, err
		}
	}

	if err = sc.migrateV1CheckpointToV2Checkpoint(checkpointV1, checkpointV2); err != nil {
		return nil, fmt.Errorf("error migrating v1 checkpoint state to v2 checkpoint state: %s", err)
	}

	return checkpointV2, nil
}

// restores state from a checkpoint and creates it if it doesn't exist
func (sc *stateCheckpoint) restoreState() error {
	sc.mux.Lock()
	defer sc.mux.Unlock()

	var (
		checkpointV2 *CPUManagerCheckpointV2
		checkpointV3 *CPUManagerCheckpointV3
		err          error
	)

	checkpointV3, err = sc.loadCheckpointV3()
	if err != nil {
		if err == errors.ErrCheckpointNotFound {
			checkpointV2, err = sc.loadAndMigrateCheckpointV2()
			if err != nil {
				if err == errors.ErrCheckpointNotFound {
					return sc.storeState()
				}
				return err
			}
		} else {
			checkpointV2, err = sc.loadAndMigrateCheckpointV2()
			if err != nil {
				return err
			}
		}
	}

	var tmpDefaultCPUSet cpuset.CPUSet
	var tmpContainerCPUSet cpuset.CPUSet
	tmpAssignments := ContainerCPUAssignments{}
	tmpContainerAssignments := ContainerAssignments{}
	tmpServiceAssignments := ServiceCPUAssignments{}
	tmpPodServiceAssignments := PodServiceAssignments{}

	switch {
	case checkpointV3 != nil:
		if sc.policyName != checkpointV3.PolicyName {
			return fmt.Errorf("configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpointV3.PolicyName)
		}
		if tmpDefaultCPUSet, err = cpuset.Parse(checkpointV3.DefaultCPUSet); err != nil {
			return fmt.Errorf("could not parse default cpu set %q: %v", checkpointV3.DefaultCPUSet, err)
		}
		for pod := range checkpointV3.Entries {
			tmpAssignments[pod] = make(map[string]cpuset.CPUSet, len(checkpointV3.Entries[pod]))
			for container, cpuString := range checkpointV3.Entries[pod] {
				if tmpContainerCPUSet, err = cpuset.Parse(cpuString); err != nil {
					return fmt.Errorf("could not parse cpuset %q for container %q in pod %q: %v", cpuString, container, pod, err)
				}
				tmpAssignments[pod][container] = tmpContainerCPUSet
			}
		}
		tmpContainerAssignments = checkpointV3.ContainerEntries.Clone()
		tmpServiceAssignments = checkpointV3.ServiceEntries.Clone()
		tmpPodServiceAssignments = checkpointV3.PodServiceEntries.Clone()
	case checkpointV2 != nil:
		if sc.policyName != checkpointV2.PolicyName {
			return fmt.Errorf("configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpointV2.PolicyName)
		}
		if tmpDefaultCPUSet, err = cpuset.Parse(checkpointV2.DefaultCPUSet); err != nil {
			return fmt.Errorf("could not parse default cpu set %q: %v", checkpointV2.DefaultCPUSet, err)
		}
		for pod := range checkpointV2.Entries {
			tmpAssignments[pod] = make(map[string]cpuset.CPUSet, len(checkpointV2.Entries[pod]))
			for container, cpuString := range checkpointV2.Entries[pod] {
				if tmpContainerCPUSet, err = cpuset.Parse(cpuString); err != nil {
					return fmt.Errorf("could not parse cpuset %q for container %q in pod %q: %v", cpuString, container, pod, err)
				}
				tmpAssignments[pod][container] = tmpContainerCPUSet
			}
		}
	default:
		return fmt.Errorf("failed to load cpu manager checkpoint")
	}

	sc.cache.SetDefaultCPUSet(tmpDefaultCPUSet)
	sc.cache.SetCPUAssignments(tmpAssignments)
	sc.cache.SetContainerAssignments(tmpContainerAssignments)
	sc.cache.SetServiceCPUAssignments(tmpServiceAssignments)
	sc.cache.SetPodServiceAssignments(tmpPodServiceAssignments)

	klog.V(2).InfoS("State checkpoint: restored state from checkpoint")
	klog.V(2).InfoS("State checkpoint: defaultCPUSet", "defaultCpuSet", tmpDefaultCPUSet.String())

	return nil
}

// saves state to a checkpoint, caller is responsible for locking
func (sc *stateCheckpoint) storeState() error {
	if len(sc.cache.GetContainerAssignments()) == 0 &&
		len(sc.cache.GetServiceCPUAssignments()) == 0 &&
		len(sc.cache.GetPodServiceAssignments()) == 0 {
		return sc.storeStateV2()
	}
	return sc.storeStateV3()
}

// saves state to a v2 checkpoint, caller is responsible for locking
func (sc *stateCheckpoint) storeStateV2() error {
	checkpoint := newCPUManagerCheckpointV2()
	checkpoint.PolicyName = sc.policyName
	checkpoint.DefaultCPUSet = sc.cache.GetDefaultCPUSet().String()

	assignments := sc.cache.GetCPUAssignments()
	for pod := range assignments {
		checkpoint.Entries[pod] = make(map[string]string, len(assignments[pod]))
		for container, cset := range assignments[pod] {
			checkpoint.Entries[pod][container] = cset.String()
		}
	}

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
	if err != nil {
		klog.ErrorS(err, "Failed to save checkpoint")
		return err
	}
	return nil
}

// saves state to a v3 checkpoint, caller is responsible for locking
func (sc *stateCheckpoint) storeStateV3() error {
	checkpoint := NewCPUManagerCheckpoint()
	checkpoint.PolicyName = sc.policyName
	checkpoint.DefaultCPUSet = sc.cache.GetDefaultCPUSet().String()

	assignments := sc.cache.GetCPUAssignments()
	for pod := range assignments {
		checkpoint.Entries[pod] = make(map[string]string, len(assignments[pod]))
		for container, cset := range assignments[pod] {
			checkpoint.Entries[pod][container] = cset.String()
		}
	}
	checkpoint.ContainerEntries = sc.cache.GetContainerAssignments()
	checkpoint.ServiceEntries = sc.cache.GetServiceCPUAssignments()
	checkpoint.PodServiceEntries = sc.cache.GetPodServiceAssignments()

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)
	if err != nil {
		klog.ErrorS(err, "Failed to save checkpoint")
		return err
	}
	return nil
}

// GetCPUSet returns current CPU set
func (sc *stateCheckpoint) GetCPUSet(podUID string, containerName string) (cpuset.CPUSet, bool) {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	res, ok := sc.cache.GetCPUSet(podUID, containerName)
	return res, ok
}

// GetDefaultCPUSet returns default CPU set
func (sc *stateCheckpoint) GetDefaultCPUSet() cpuset.CPUSet {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetDefaultCPUSet()
}

// GetCPUSetOrDefault returns current CPU set, or default one if it wasn't changed
func (sc *stateCheckpoint) GetCPUSetOrDefault(podUID string, containerName string) cpuset.CPUSet {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetCPUSetOrDefault(podUID, containerName)
}

// GetCPUAssignments returns current CPU to pod assignments
func (sc *stateCheckpoint) GetCPUAssignments() ContainerCPUAssignments {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetCPUAssignments()
}

func (sc *stateCheckpoint) GetContainerAssignment(podUID string, containerName string) (ContainerAssignment, bool) {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetContainerAssignment(podUID, containerName)
}

func (sc *stateCheckpoint) GetContainerAssignments() ContainerAssignments {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetContainerAssignments()
}

func (sc *stateCheckpoint) GetServiceCPUAssignment(service string) (ServiceCPUAssignment, bool) {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetServiceCPUAssignment(service)
}

func (sc *stateCheckpoint) GetServiceCPUAssignments() ServiceCPUAssignments {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetServiceCPUAssignments()
}

func (sc *stateCheckpoint) GetPodServiceAssignment(podUID string) (PodServiceAssignment, bool) {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetPodServiceAssignment(podUID)
}

func (sc *stateCheckpoint) GetPodServiceAssignments() PodServiceAssignments {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetPodServiceAssignments()
}

// SetCPUSet sets CPU set
func (sc *stateCheckpoint) SetCPUSet(podUID string, containerName string, cset cpuset.CPUSet) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetCPUSet(podUID, containerName, cset)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

// SetDefaultCPUSet sets default CPU set
func (sc *stateCheckpoint) SetDefaultCPUSet(cset cpuset.CPUSet) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetDefaultCPUSet(cset)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

// SetCPUAssignments sets CPU to pod assignments
func (sc *stateCheckpoint) SetCPUAssignments(a ContainerCPUAssignments) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetCPUAssignments(a)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

func (sc *stateCheckpoint) SetContainerAssignment(podUID string, containerName string, assignment ContainerAssignment) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetContainerAssignment(podUID, containerName, assignment)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

func (sc *stateCheckpoint) SetContainerAssignments(assignments ContainerAssignments) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetContainerAssignments(assignments)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

func (sc *stateCheckpoint) SetServiceCPUAssignment(service string, assignment ServiceCPUAssignment) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetServiceCPUAssignment(service, assignment)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

func (sc *stateCheckpoint) SetServiceCPUAssignments(assignments ServiceCPUAssignments) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetServiceCPUAssignments(assignments)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

func (sc *stateCheckpoint) DeleteServiceCPUAssignment(service string) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.DeleteServiceCPUAssignment(service)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

func (sc *stateCheckpoint) SetPodServiceAssignment(podUID string, assignment PodServiceAssignment) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetPodServiceAssignment(podUID, assignment)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

func (sc *stateCheckpoint) SetPodServiceAssignments(assignments PodServiceAssignments) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetPodServiceAssignments(assignments)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

func (sc *stateCheckpoint) DeletePodServiceAssignment(podUID string) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.DeletePodServiceAssignment(podUID)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

// Delete deletes assignment for specified pod
func (sc *stateCheckpoint) Delete(podUID string, containerName string) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.Delete(podUID, containerName)
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}

// ClearState clears the state and saves it in a checkpoint
func (sc *stateCheckpoint) ClearState() {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.ClearState()
	err := sc.storeState()
	if err != nil {
		klog.InfoS("Store state to checkpoint error", "err", err)
	}
}
