//go:build linux
// +build linux

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
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

type rawBPFAppCPUSetOps struct{}

type bpfAttrObjGet struct {
	Pathname  uint64
	BpfFd     uint32
	FileFlags uint32
}

type bpfAttrMapElem struct {
	MapFd uint32
	_     uint32
	Key   uint64
	Value uint64
	Flags uint64
}

func newBPFAppCPUSetOps() bpfAppCPUSetOps {
	return rawBPFAppCPUSetOps{}
}

func (rawBPFAppCPUSetOps) ObjGet(path string) (int, error) {
	pathname, err := unix.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	attr := bpfAttrObjGet{
		Pathname: uint64(uintptr(unsafe.Pointer(pathname))),
	}
	fd, err := bpfSyscall(unix.BPF_OBJ_GET, unsafe.Pointer(&attr), unsafe.Sizeof(attr))
	runtime.KeepAlive(pathname)
	if err != nil {
		return 0, err
	}
	return int(fd), nil
}

func (rawBPFAppCPUSetOps) LookupElem(fd int, key *scxAppKey, value *scxAppCPUSet) error {
	attr := bpfAttrMapElem{
		MapFd: uint32(fd),
		Key:   uint64(uintptr(unsafe.Pointer(key))),
		Value: uint64(uintptr(unsafe.Pointer(value))),
	}
	_, err := bpfSyscall(unix.BPF_MAP_LOOKUP_ELEM, unsafe.Pointer(&attr), unsafe.Sizeof(attr))
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return errBPFMapKeyNotFound
		}
		return err
	}
	return nil
}

func (rawBPFAppCPUSetOps) UpdateElem(fd int, key *scxAppKey, value *scxAppCPUSet) error {
	attr := bpfAttrMapElem{
		MapFd: uint32(fd),
		Key:   uint64(uintptr(unsafe.Pointer(key))),
		Value: uint64(uintptr(unsafe.Pointer(value))),
		Flags: unix.BPF_ANY,
	}
	_, err := bpfSyscall(unix.BPF_MAP_UPDATE_ELEM, unsafe.Pointer(&attr), unsafe.Sizeof(attr))
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
	return err
}

func (rawBPFAppCPUSetOps) Close(fd int) error {
	return unix.Close(fd)
}

func bpfSyscall(cmd int, attr unsafe.Pointer, size uintptr) (uintptr, error) {
	ret, _, errno := unix.Syscall(unix.SYS_BPF, uintptr(cmd), uintptr(attr), size)
	if errno != 0 {
		return 0, errno
	}
	return ret, nil
}
