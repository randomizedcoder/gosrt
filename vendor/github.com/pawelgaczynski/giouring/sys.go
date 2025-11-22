// MIT License
//
// Copyright (c) 2023 Paweł Gaczyński
//
// Permission is hereby granted, free of charge, to any person obtaining a
// copy of this software and associated documentation files (the
// "Software"), to deal in the Software without restriction, including
// without limitation the rights to use, copy, modify, merge, publish,
// distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to
// the following conditions:
//
// The above copyright notice and this permission notice shall be included
// in all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS
// OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
// IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
// CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
// TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
// SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package giouring

import (
	"math"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func sysMmap(addr, length uintptr, prot, flags, fd int, offset int64) (unsafe.Pointer, error) {
	// Use golang.org/x/sys/unix.MmapPtr which works with CGO_ENABLED=0
	ptr, err := unix.MmapPtr(fd, offset, unsafe.Pointer(addr), length, prot, flags)
	if err != nil {
		return nil, err
	}
	return ptr, nil
}

func sysMunmap(addr, length uintptr) error {
	// Use golang.org/x/sys/unix.MunmapPtr which works with CGO_ENABLED=0
	return unix.MunmapPtr(unsafe.Pointer(addr), length)
}

// mmap is a wrapper that matches the old signature for direct calls in setup.go
func mmap(addr, length uintptr, prot, flags, fd int, offset int64) (uintptr, error) {
	ptr, err := sysMmap(addr, length, prot, flags, fd, offset)
	if err != nil {
		return 0, err
	}
	return uintptr(ptr), nil
}

func sysMadvise(address, length, advice uintptr) error {
	_, _, err := syscall.Syscall(syscall.SYS_MADVISE, address, length, advice)

	return err
}

const liburingUdataTimeout uint64 = math.MaxUint64
