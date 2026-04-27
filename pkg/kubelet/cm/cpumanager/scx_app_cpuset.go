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

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

const (
	scxAwesomeAppNameLen = 64
	scxAwesomeMaxCPUs    = 128
	scxAwesomeCPUWords   = scxAwesomeMaxCPUs / 64
)

var errBPFMapKeyNotFound = errors.New("BPF map key not found")

type appCPUSetStore interface {
	Set(app string, cset cpuset.CPUSet) error
	Clear(app string) error
}

type bpfAppCPUSetOps interface {
	ObjGet(path string) (int, error)
	LookupElem(fd int, key *scxAppKey, value *scxAppCPUSet) error
	UpdateElem(fd int, key *scxAppKey, value *scxAppCPUSet) error
	Close(fd int) error
}

type bpfAppCPUSetStore struct {
	mapPath string
	ops     bpfAppCPUSetOps
}

type scxAppKey struct {
	Name [scxAwesomeAppNameLen]byte
}

type scxAppCPUSet struct {
	Valid       uint32
	Generation  uint32
	NrCPUIds    uint32
	Weight      uint32
	AllowedBits [scxAwesomeCPUWords]uint64
}

func newBPFAppCPUSetStore(mapPath string) appCPUSetStore {
	return newBPFAppCPUSetStoreWithOps(mapPath, newBPFAppCPUSetOps())
}

func newBPFAppCPUSetStoreWithOps(mapPath string, ops bpfAppCPUSetOps) *bpfAppCPUSetStore {
	return &bpfAppCPUSetStore{
		mapPath: mapPath,
		ops:     ops,
	}
}

func newSCXAppKey(app string) (scxAppKey, error) {
	var key scxAppKey
	if app == "" {
		return key, fmt.Errorf("app name must not be empty")
	}
	if strings.ContainsRune(app, 0) {
		return key, fmt.Errorf("app name %q must not contain NUL bytes", app)
	}
	if len([]byte(app)) >= scxAwesomeAppNameLen {
		return key, fmt.Errorf("app name %q is too long: got %d bytes, maximum is %d", app, len([]byte(app)), scxAwesomeAppNameLen-1)
	}
	copy(key.Name[:], app)
	return key, nil
}

func scxAppCPUSetFromCPUSet(cset cpuset.CPUSet) (scxAppCPUSet, error) {
	value := scxAppCPUSet{
		Valid:    1,
		NrCPUIds: scxAwesomeMaxCPUs,
	}
	if cset.IsEmpty() {
		return value, fmt.Errorf("cpuset must not be empty")
	}
	for _, cpu := range cset.UnsortedList() {
		if cpu < 0 || cpu >= scxAwesomeMaxCPUs {
			return value, fmt.Errorf("cpu id %d is outside supported sched_ext app cpuset range [0,%d)", cpu, scxAwesomeMaxCPUs)
		}
		word := cpu / 64
		bit := uint(cpu % 64)
		value.AllowedBits[word] |= uint64(1) << bit
		value.Weight++
	}
	return value, nil
}

func clearSCXAppCPUSet(oldGeneration uint32) scxAppCPUSet {
	return scxAppCPUSet{
		Generation: oldGeneration + 1,
		NrCPUIds:   scxAwesomeMaxCPUs,
	}
}

func (s *bpfAppCPUSetStore) Set(app string, cset cpuset.CPUSet) error {
	key, err := newSCXAppKey(app)
	if err != nil {
		return err
	}
	value, err := scxAppCPUSetFromCPUSet(cset)
	if err != nil {
		return err
	}

	if err := s.withMapFD(func(fd int) error {
		generation, err := s.lookupGeneration(fd, &key)
		if err != nil {
			return err
		}
		value.Generation = generation + 1
		if err := s.ops.UpdateElem(fd, &key, &value); err != nil {
			return fmt.Errorf("update BPF app cpuset map %q for app %q: %w", s.mapPath, app, err)
		}
		return nil
	}); err != nil {
		return err
	}

	klog.V(4).InfoS("Updated sched_ext app cpuset BPF map", "app", app, "cpuSet", cset, "generation", value.Generation, "mapPath", s.mapPath)
	return nil
}

func (s *bpfAppCPUSetStore) Clear(app string) error {
	key, err := newSCXAppKey(app)
	if err != nil {
		return err
	}

	var value scxAppCPUSet
	if err := s.withMapFD(func(fd int) error {
		generation, err := s.lookupGeneration(fd, &key)
		if err != nil {
			return err
		}
		value = clearSCXAppCPUSet(generation)
		if err := s.ops.UpdateElem(fd, &key, &value); err != nil {
			return fmt.Errorf("clear BPF app cpuset map %q for app %q: %w", s.mapPath, app, err)
		}
		return nil
	}); err != nil {
		return err
	}

	klog.V(4).InfoS("Cleared sched_ext app cpuset BPF map", "app", app, "generation", value.Generation, "mapPath", s.mapPath)
	return nil
}

func (s *bpfAppCPUSetStore) lookupGeneration(fd int, key *scxAppKey) (uint32, error) {
	var old scxAppCPUSet
	if err := s.ops.LookupElem(fd, key, &old); err != nil {
		if errors.Is(err, errBPFMapKeyNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("lookup BPF app cpuset map %q generation: %w", s.mapPath, err)
	}
	return old.Generation, nil
}

func (s *bpfAppCPUSetStore) withMapFD(fn func(fd int) error) (err error) {
	fd, err := s.ops.ObjGet(s.mapPath)
	if err != nil {
		return fmt.Errorf("open BPF app cpuset map %q: %w", s.mapPath, err)
	}
	defer func() {
		if closeErr := s.ops.Close(fd); err == nil && closeErr != nil {
			err = fmt.Errorf("close BPF app cpuset map %q: %w", s.mapPath, closeErr)
		}
	}()
	return fn(fd)
}
