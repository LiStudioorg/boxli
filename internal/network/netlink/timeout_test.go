// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package netlink

import (
	"syscall"
	"testing"
	"unsafe"
)

// TestSocketHasRecvTimeout 回归 Bug 6：rtnetlink 套接字必须带接收超时，
// 否则内核不回应时 Recvfrom 永久阻塞，卡死 stop/rm 等一连串 netlink 操作。
func TestSocketHasRecvTimeout(t *testing.T) {
	fd, err := socket()
	if err != nil {
		t.Skipf("无 NETLINK_ROUTE 权限，跳过: %v", err)
	}
	var tv [16]byte
	optlen := 16
	_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(fd),
		uintptr(syscall.SOL_SOCKET), uintptr(syscall.SO_RCVTIMEO),
		uintptr(unsafe.Pointer(&tv[0])), uintptr(unsafe.Pointer(&optlen)), 0)
	if errno != 0 {
		t.Fatalf("getsockopt SO_RCVTIMEO: %v", errno)
	}
	nonzero := false
	for _, b := range tv {
		if b != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("SO_RCVTIMEO 全 0：Recvfrom 可能无限阻塞")
	}
}
