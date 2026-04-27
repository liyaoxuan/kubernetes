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
	"strings"
	"testing"
	"unsafe"

	"k8s.io/utils/cpuset"
)

func TestSCXAppStructLayout(t *testing.T) {
	if got := unsafe.Sizeof(scxAppKey{}); got != scxAwesomeAppNameLen {
		t.Fatalf("unexpected scxAppKey size: got %d want %d", got, scxAwesomeAppNameLen)
	}
	if got := unsafe.Sizeof(scxAppCPUSet{}); got != 32 {
		t.Fatalf("unexpected scxAppCPUSet size: got %d want 32", got)
	}
	if got := unsafe.Offsetof(scxAppCPUSet{}.AllowedBits); got != 16 {
		t.Fatalf("unexpected scxAppCPUSet AllowedBits offset: got %d want 16", got)
	}
}

func TestNewSCXAppKey(t *testing.T) {
	key, err := newSCXAppKey("ali-app")
	if err != nil {
		t.Fatalf("unexpected app key error: %v", err)
	}
	if got := string(key.Name[:7]); got != "ali-app" {
		t.Fatalf("unexpected key name prefix: got %q", got)
	}
	if key.Name[7] != 0 {
		t.Fatalf("expected key name to remain NUL padded")
	}

	testCases := []struct {
		name string
		app  string
	}{
		{
			name: "empty",
			app:  "",
		},
		{
			name: "too long",
			app:  strings.Repeat("a", scxAwesomeAppNameLen),
		},
		{
			name: "contains NUL",
			app:  "ali\x00app",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newSCXAppKey(tc.app); err == nil {
				t.Fatalf("expected app key error")
			}
		})
	}
}

func TestSCXAppCPUSetFromCPUSet(t *testing.T) {
	value, err := scxAppCPUSetFromCPUSet(cpuset.New(0, 1, 63, 64, 127))
	if err != nil {
		t.Fatalf("unexpected cpuset conversion error: %v", err)
	}
	if value.Valid != 1 {
		t.Fatalf("expected valid cpuset value, got %d", value.Valid)
	}
	if value.NrCPUIds != scxAwesomeMaxCPUs {
		t.Fatalf("unexpected nr_cpu_ids: got %d want %d", value.NrCPUIds, scxAwesomeMaxCPUs)
	}
	if value.Weight != 5 {
		t.Fatalf("unexpected weight: got %d want 5", value.Weight)
	}
	if want := uint64(1)<<0 | uint64(1)<<1 | uint64(1)<<63; value.AllowedBits[0] != want {
		t.Fatalf("unexpected word 0 bitmap: got %#x want %#x", value.AllowedBits[0], want)
	}
	if want := uint64(1)<<0 | uint64(1)<<63; value.AllowedBits[1] != want {
		t.Fatalf("unexpected word 1 bitmap: got %#x want %#x", value.AllowedBits[1], want)
	}
}

func TestSCXAppCPUSetFromCPUSetValidation(t *testing.T) {
	testCases := []struct {
		name string
		cset cpuset.CPUSet
	}{
		{
			name: "empty",
			cset: cpuset.New(),
		},
		{
			name: "negative CPU",
			cset: cpuset.New(-1),
		},
		{
			name: "CPU outside sched_ext map range",
			cset: cpuset.New(scxAwesomeMaxCPUs),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := scxAppCPUSetFromCPUSet(tc.cset); err == nil {
				t.Fatalf("expected cpuset conversion error")
			}
		})
	}
}

func TestBPFAppCPUSetStoreSetUsesNextGeneration(t *testing.T) {
	ops := &fakeBPFAppCPUSetOps{
		fd:          7,
		lookupValue: scxAppCPUSet{Generation: 41},
	}
	store := newBPFAppCPUSetStoreWithOps("/sys/fs/bpf/scx_awesome/app_cpuset", ops)

	if err := store.Set("ali-app", cpuset.New(2, 64)); err != nil {
		t.Fatalf("unexpected set error: %v", err)
	}
	if len(ops.updates) != 1 {
		t.Fatalf("expected one update, got %d", len(ops.updates))
	}
	update := ops.updates[0]
	if update.Generation != 42 {
		t.Fatalf("unexpected generation: got %d want 42", update.Generation)
	}
	if update.Valid != 1 || update.NrCPUIds != scxAwesomeMaxCPUs || update.Weight != 2 {
		t.Fatalf("unexpected updated value: %#v", update)
	}
	if got := fakeAppNameFromKey(ops.updateKeys[0]); got != "ali-app" {
		t.Fatalf("unexpected update key: got %q", got)
	}
	if !reflectIntSlicesEqual(ops.closed, []int{7}) {
		t.Fatalf("expected map fd to be closed, got %v", ops.closed)
	}
}

func TestBPFAppCPUSetStoreSetMissingEntryStartsAtGenerationOne(t *testing.T) {
	ops := &fakeBPFAppCPUSetOps{
		fd:        7,
		lookupErr: errBPFMapKeyNotFound,
	}
	store := newBPFAppCPUSetStoreWithOps("/sys/fs/bpf/scx_awesome/app_cpuset", ops)

	if err := store.Set("ali-app", cpuset.New(2)); err != nil {
		t.Fatalf("unexpected set error: %v", err)
	}
	if len(ops.updates) != 1 {
		t.Fatalf("expected one update, got %d", len(ops.updates))
	}
	if got := ops.updates[0].Generation; got != 1 {
		t.Fatalf("unexpected generation: got %d want 1", got)
	}
}

func TestBPFAppCPUSetStoreClearUsesNextGeneration(t *testing.T) {
	ops := &fakeBPFAppCPUSetOps{
		fd:          7,
		lookupValue: scxAppCPUSet{Generation: 5},
	}
	store := newBPFAppCPUSetStoreWithOps("/sys/fs/bpf/scx_awesome/app_cpuset", ops)

	if err := store.Clear("ali-app"); err != nil {
		t.Fatalf("unexpected clear error: %v", err)
	}
	if len(ops.updates) != 1 {
		t.Fatalf("expected one update, got %d", len(ops.updates))
	}
	update := ops.updates[0]
	if update.Valid != 0 || update.Generation != 6 || update.NrCPUIds != scxAwesomeMaxCPUs || update.Weight != 0 {
		t.Fatalf("unexpected clear value: %#v", update)
	}
	if update.AllowedBits[0] != 0 || update.AllowedBits[1] != 0 {
		t.Fatalf("expected clear bitmap to be zeroed, got %#v", update.AllowedBits)
	}
}

func TestBPFAppCPUSetStoreKeepsRetryStateOnUpdateFailure(t *testing.T) {
	updateErr := errors.New("update failed")
	ops := &fakeBPFAppCPUSetOps{
		fd:        7,
		updateErr: updateErr,
	}
	store := newBPFAppCPUSetStoreWithOps("/sys/fs/bpf/scx_awesome/app_cpuset", ops)

	if err := store.Set("ali-app", cpuset.New(2)); !errors.Is(err, updateErr) {
		t.Fatalf("expected update error, got %v", err)
	}
	if len(ops.updates) != 1 {
		t.Fatalf("expected one attempted update, got %d", len(ops.updates))
	}
	if !reflectIntSlicesEqual(ops.closed, []int{7}) {
		t.Fatalf("expected map fd to be closed on error, got %v", ops.closed)
	}
}

type fakeBPFAppCPUSetOps struct {
	fd          int
	objGetErr   error
	lookupValue scxAppCPUSet
	lookupErr   error
	updateErr   error
	closeErr    error

	paths      []string
	lookupKeys []scxAppKey
	updateKeys []scxAppKey
	updates    []scxAppCPUSet
	closed     []int
}

func (f *fakeBPFAppCPUSetOps) ObjGet(path string) (int, error) {
	f.paths = append(f.paths, path)
	if f.objGetErr != nil {
		return 0, f.objGetErr
	}
	return f.fd, nil
}

func (f *fakeBPFAppCPUSetOps) LookupElem(fd int, key *scxAppKey, value *scxAppCPUSet) error {
	f.lookupKeys = append(f.lookupKeys, *key)
	if f.lookupErr != nil {
		return f.lookupErr
	}
	*value = f.lookupValue
	return nil
}

func (f *fakeBPFAppCPUSetOps) UpdateElem(fd int, key *scxAppKey, value *scxAppCPUSet) error {
	f.updateKeys = append(f.updateKeys, *key)
	f.updates = append(f.updates, *value)
	return f.updateErr
}

func (f *fakeBPFAppCPUSetOps) Close(fd int) error {
	f.closed = append(f.closed, fd)
	return f.closeErr
}

func fakeAppNameFromKey(key scxAppKey) string {
	length := 0
	for length < len(key.Name) && key.Name[length] != 0 {
		length++
	}
	return string(key.Name[:length])
}

func reflectIntSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
