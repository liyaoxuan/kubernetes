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
	"encoding/json"
	"fmt"

	"k8s.io/utils/cpuset"
)

// ContainerCPUAssignments type used in cpu manager state
type ContainerCPUAssignments map[string]map[string]cpuset.CPUSet

// Clone returns a copy of ContainerCPUAssignments
func (as ContainerCPUAssignments) Clone() ContainerCPUAssignments {
	ret := make(ContainerCPUAssignments, len(as))
	for pod := range as {
		ret[pod] = make(map[string]cpuset.CPUSet, len(as[pod]))
		for container, cset := range as[pod] {
			ret[pod][container] = cset
		}
	}
	return ret
}

// CPUAssignmentType describes how an assigned cpuset should be interpreted.
type CPUAssignmentType string

const (
	CPUAssignmentExclusive   CPUAssignmentType = "exclusive"
	CPUAssignmentServicePool CPUAssignmentType = "service-pooled"
)

// ContainerAssignments stores metadata for container-level CPU assignments.
type ContainerAssignments map[string]map[string]ContainerAssignment

// ContainerAssignment stores metadata about a single container assignment.
type ContainerAssignment struct {
	AssignmentType CPUAssignmentType `json:"assignmentType,omitempty"`
}

// Clone returns a copy of ContainerAssignments.
func (a ContainerAssignments) Clone() ContainerAssignments {
	clone := make(ContainerAssignments, len(a))
	for podUID := range a {
		clone[podUID] = make(map[string]ContainerAssignment, len(a[podUID]))
		for containerName, assignment := range a[podUID] {
			clone[podUID][containerName] = assignment
		}
	}
	return clone
}

// ServiceCPUAssignments stores node-local service pool assignments.
type ServiceCPUAssignments map[string]ServiceCPUAssignment

// ServiceCPUAssignment stores the cpuset and target size for a service pool.
type ServiceCPUAssignment struct {
	CPUSet        cpuset.CPUSet `json:"cpuSet,omitempty"`
	RequestedCPUs int           `json:"requestedCPUs,omitempty"`
}

type serviceCPUAssignmentJSON struct {
	CPUSet        string `json:"cpuSet,omitempty"`
	RequestedCPUs int    `json:"requestedCPUs,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (a ServiceCPUAssignment) MarshalJSON() ([]byte, error) {
	return json.Marshal(serviceCPUAssignmentJSON{
		CPUSet:        a.CPUSet.String(),
		RequestedCPUs: a.RequestedCPUs,
	})
}

// UnmarshalJSON implements json.Unmarshaler.
func (a *ServiceCPUAssignment) UnmarshalJSON(b []byte) error {
	var entry serviceCPUAssignmentJSON
	if err := json.Unmarshal(b, &entry); err != nil {
		return err
	}
	cset, err := cpuset.Parse(entry.CPUSet)
	if err != nil {
		return fmt.Errorf("failed to parse cpuset: %w", err)
	}
	a.CPUSet = cset
	a.RequestedCPUs = entry.RequestedCPUs
	return nil
}

// Clone returns a copy of ServiceCPUAssignments.
func (a ServiceCPUAssignments) Clone() ServiceCPUAssignments {
	clone := make(ServiceCPUAssignments, len(a))
	for service, assignment := range a {
		clone[service] = assignment
	}
	return clone
}

// PodServiceAssignments stores per-pod service-pool membership snapshots.
type PodServiceAssignments map[string]PodServiceAssignment

// PodServiceAssignment stores the service and CPU contribution for a pod.
type PodServiceAssignment struct {
	Service       string `json:"service,omitempty"`
	RequestedCPUs int    `json:"requestedCPUs,omitempty"`
}

// Clone returns a copy of PodServiceAssignments.
func (a PodServiceAssignments) Clone() PodServiceAssignments {
	clone := make(PodServiceAssignments, len(a))
	for podUID, assignment := range a {
		clone[podUID] = assignment
	}
	return clone
}

// Reader interface used to read current cpu/pod assignment state
type Reader interface {
	GetCPUSet(podUID string, containerName string) (cpuset.CPUSet, bool)
	GetDefaultCPUSet() cpuset.CPUSet
	GetCPUSetOrDefault(podUID string, containerName string) cpuset.CPUSet
	GetCPUAssignments() ContainerCPUAssignments
	GetContainerAssignment(podUID string, containerName string) (ContainerAssignment, bool)
	GetContainerAssignments() ContainerAssignments
	GetServiceCPUAssignment(service string) (ServiceCPUAssignment, bool)
	GetServiceCPUAssignments() ServiceCPUAssignments
	GetPodServiceAssignment(podUID string) (PodServiceAssignment, bool)
	GetPodServiceAssignments() PodServiceAssignments
}

type writer interface {
	SetCPUSet(podUID string, containerName string, cpuset cpuset.CPUSet)
	SetDefaultCPUSet(cpuset cpuset.CPUSet)
	SetCPUAssignments(ContainerCPUAssignments)
	SetContainerAssignment(podUID string, containerName string, assignment ContainerAssignment)
	SetContainerAssignments(ContainerAssignments)
	SetServiceCPUAssignment(service string, assignment ServiceCPUAssignment)
	SetServiceCPUAssignments(ServiceCPUAssignments)
	DeleteServiceCPUAssignment(service string)
	SetPodServiceAssignment(podUID string, assignment PodServiceAssignment)
	SetPodServiceAssignments(PodServiceAssignments)
	DeletePodServiceAssignment(podUID string)
	Delete(podUID string, containerName string)
	ClearState()
}

// State interface provides methods for tracking and setting cpu/pod assignment
type State interface {
	Reader
	writer
}
