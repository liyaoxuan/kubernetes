/*
Copyright 2017 The Kubernetes Authors.

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
	"maps"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

type stateMemory struct {
	sync.RWMutex
	logger               logr.Logger
	assignments          ContainerCPUAssignments
	containerAssignments ContainerAssignments
	podAssignments       PodCPUAssignments
	serviceAssignments   ServiceCPUAssignments
	podServiceAssignments PodServiceAssignments
	defaultCPUSet        cpuset.CPUSet
}

var _ State = &stateMemory{}

// NewMemoryState creates new State for keeping track of cpu/pod assignment
func NewMemoryState(logger logr.Logger) State {
	// we store a logger instance to be consistent with the CheckpointState interface (see comments there)
	// since we store a checkpoint, we can use the relatively expensive "WithName".
	logger = klog.LoggerWithName(logger, "CPUManager state memory")
	logger.Info("Initialized")

	return &stateMemory{
		logger:                logger,
		assignments:           ContainerCPUAssignments{},
		containerAssignments:  ContainerAssignments{},
		podAssignments:        PodCPUAssignments{},
		serviceAssignments:    ServiceCPUAssignments{},
		podServiceAssignments: PodServiceAssignments{},
		defaultCPUSet:         cpuset.New(),
	}
}

func (s *stateMemory) GetCPUSet(podUID string, containerName string) (cpuset.CPUSet, bool) {
	s.RLock()
	defer s.RUnlock()

	res, ok := s.assignments[podUID][containerName]
	return res.Clone(), ok
}

func (s *stateMemory) GetDefaultCPUSet() cpuset.CPUSet {
	s.RLock()
	defer s.RUnlock()

	return s.defaultCPUSet.Clone()
}

func (s *stateMemory) GetCPUSetOrDefault(podUID string, containerName string) cpuset.CPUSet {
	if res, ok := s.GetCPUSet(podUID, containerName); ok {
		return res
	}
	return s.GetDefaultCPUSet()
}

func (s *stateMemory) GetCPUAssignments() ContainerCPUAssignments {
	s.RLock()
	defer s.RUnlock()
	return s.assignments.Clone()
}

func (s *stateMemory) GetContainerAssignment(podUID string, containerName string) (ContainerAssignment, bool) {
	s.RLock()
	defer s.RUnlock()

	res, ok := s.containerAssignments[podUID][containerName]
	return res, ok
}

func (s *stateMemory) GetContainerAssignments() ContainerAssignments {
	s.RLock()
	defer s.RUnlock()
	return s.containerAssignments.Clone()
}

func (s *stateMemory) GetPodCPUSet(podUID string) (cpuset.CPUSet, bool) {
	s.RLock()
	defer s.RUnlock()
	res, ok := s.podAssignments[podUID]
	return res.CPUSet.Clone(), ok
}

func (s *stateMemory) GetPodCPUAssignments() PodCPUAssignments {
	s.RLock()
	defer s.RUnlock()
	clone := make(PodCPUAssignments)
	maps.Copy(clone, s.podAssignments)
	return clone
}

func (s *stateMemory) GetServiceCPUAssignment(service string) (ServiceCPUAssignment, bool) {
	s.RLock()
	defer s.RUnlock()

	res, ok := s.serviceAssignments[service]
	return res, ok
}

func (s *stateMemory) GetServiceCPUAssignments() ServiceCPUAssignments {
	s.RLock()
	defer s.RUnlock()
	return s.serviceAssignments.Clone()
}

func (s *stateMemory) GetPodServiceAssignment(podUID string) (PodServiceAssignment, bool) {
	s.RLock()
	defer s.RUnlock()

	res, ok := s.podServiceAssignments[podUID]
	return res, ok
}

func (s *stateMemory) GetPodServiceAssignments() PodServiceAssignments {
	s.RLock()
	defer s.RUnlock()
	return s.podServiceAssignments.Clone()
}

func (s *stateMemory) SetCPUSet(podUID string, containerName string, cset cpuset.CPUSet) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.assignments[podUID]; !ok {
		s.assignments[podUID] = make(map[string]cpuset.CPUSet)
	}

	s.assignments[podUID][containerName] = cset
	s.logger.Info("Updated desired CPUSet", "podUID", podUID, "containerName", containerName, "cpuSet", cset)
}

func (s *stateMemory) SetDefaultCPUSet(cset cpuset.CPUSet) {
	s.Lock()
	defer s.Unlock()

	s.defaultCPUSet = cset
	s.logger.Info("Updated default CPUSet", "cpuSet", cset)
}

func (s *stateMemory) SetPodCPUSet(podUID string, cset cpuset.CPUSet) {
	s.Lock()
	defer s.Unlock()

	podEntry := s.podAssignments[podUID]
	podEntry.CPUSet = cset
	s.podAssignments[podUID] = podEntry
	s.logger.Info("Updated pod CPUSet", "podUID", podUID, "cpuSet", cset)
}

func (s *stateMemory) SetCPUAssignments(a ContainerCPUAssignments) {
	s.Lock()
	defer s.Unlock()

	s.assignments = a.Clone()
	s.logger.Info("Updated CPUSet assignments", "assignments", a)
}

func (s *stateMemory) SetContainerAssignment(podUID string, containerName string, assignment ContainerAssignment) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.containerAssignments[podUID]; !ok {
		s.containerAssignments[podUID] = make(map[string]ContainerAssignment)
	}

	s.containerAssignments[podUID][containerName] = assignment
	s.logger.Info("Updated container CPU assignment metadata", "podUID", podUID, "containerName", containerName, "assignment", assignment)
}

func (s *stateMemory) SetContainerAssignments(assignments ContainerAssignments) {
	s.Lock()
	defer s.Unlock()

	s.containerAssignments = assignments.Clone()
	s.logger.Info("Updated container CPU assignment metadata", "assignments", assignments)
}

func (s *stateMemory) SetPodCPUAssignments(a PodCPUAssignments) {
	s.Lock()
	defer s.Unlock()

	s.podAssignments = make(PodCPUAssignments)
	maps.Copy(s.podAssignments, a)
	s.logger.Info("Updated pod CPUSet assignments", "assignments", a)
}

func (s *stateMemory) SetServiceCPUAssignment(service string, assignment ServiceCPUAssignment) {
	s.Lock()
	defer s.Unlock()

	s.serviceAssignments[service] = assignment
	s.logger.Info("Updated service CPU assignment", "service", service, "assignment", assignment)
}

func (s *stateMemory) SetServiceCPUAssignments(assignments ServiceCPUAssignments) {
	s.Lock()
	defer s.Unlock()

	s.serviceAssignments = assignments.Clone()
	s.logger.Info("Updated service CPU assignments", "assignments", assignments)
}

func (s *stateMemory) DeleteServiceCPUAssignment(service string) {
	s.Lock()
	defer s.Unlock()

	delete(s.serviceAssignments, service)
	s.logger.V(2).Info("Deleted service CPU assignment", "service", service)
}

func (s *stateMemory) SetPodServiceAssignment(podUID string, assignment PodServiceAssignment) {
	s.Lock()
	defer s.Unlock()

	s.podServiceAssignments[podUID] = assignment
	s.logger.Info("Updated pod service assignment", "podUID", podUID, "assignment", assignment)
}

func (s *stateMemory) SetPodServiceAssignments(assignments PodServiceAssignments) {
	s.Lock()
	defer s.Unlock()

	s.podServiceAssignments = assignments.Clone()
	s.logger.Info("Updated pod service assignments", "assignments", assignments)
}

func (s *stateMemory) DeletePodServiceAssignment(podUID string) {
	s.Lock()
	defer s.Unlock()

	delete(s.podServiceAssignments, podUID)
	s.logger.V(2).Info("Deleted pod service assignment", "podUID", podUID)
}

func (s *stateMemory) Delete(podUID string, containerName string) {
	s.Lock()
	defer s.Unlock()

	delete(s.assignments[podUID], containerName)
	if len(s.assignments[podUID]) == 0 {
		delete(s.assignments, podUID)
	}
	delete(s.containerAssignments[podUID], containerName)
	if len(s.containerAssignments[podUID]) == 0 {
		delete(s.containerAssignments, podUID)
	}
	s.logger.V(2).Info("Deleted CPUSet assignment", "podUID", podUID, "containerName", containerName)
}

// DeletePod deletes pod-level CPU assignments for specified pod. It does not
// affect container-level assignments.
func (s *stateMemory) DeletePod(podUID string) {
	s.Lock()
	defer s.Unlock()

	delete(s.podAssignments, podUID)
	delete(s.podServiceAssignments, podUID)
	s.logger.V(2).Info("Deleted pod", "podUID", podUID)
}

func (s *stateMemory) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.defaultCPUSet = cpuset.New()
	s.assignments = make(ContainerCPUAssignments)
	s.containerAssignments = make(ContainerAssignments)
	s.podAssignments = make(PodCPUAssignments)
	s.serviceAssignments = make(ServiceCPUAssignments)
	s.podServiceAssignments = make(PodServiceAssignments)
	s.logger.V(2).Info("Cleared state")
}
