// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package netlink

import (
	"errors"
	"syscall"
	"testing"
)

// mkPair 返回一对 AF_UNIX SOCK_DGRAM 套接字（向 recvLoop 注入应答）。
func mkPair(t *testing.T) (int, int) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fds[0]); _ = syscall.Close(fds[1]) })
	return fds[0], fds[1]
}

func nlmsgBytes(msgType uint16, seq uint32, payload []byte) []byte {
	b := make([]byte, 16+len(payload))
	putU32(b[0:4], uint32(len(b)))
	putU16(b[4:6], msgType)
	putU16(b[6:8], 0)
	putU32(b[8:12], seq)
	copy(b[16:], payload)
	return b
}

// ackMsg 构造 NLMSG_ERROR(code=0) 的 ACK。
func ackMsg(seq uint32) []byte {
	p := make([]byte, 4+16)
	putU32(p[0:4], 0) // code=0 = ACK
	return nlmsgBytes(NLmsgError, seq, p)
}

// errMsg 构造 NLMSG_ERROR(code=<neg errno>) 的错误应答。
func errMsg(seq uint32, code int32) []byte {
	p := make([]byte, 4+16)
	putU32(p[0:4], uint32(code))
	return nlmsgBytes(NLmsgError, seq, p)
}

// TestRecvLoopEAGAIN 错误注入：非阻塞空 socket → Recvfrom 立即 EAGAIN，
// 必须返回带"等待应答超时"的明确错误。
func TestRecvLoopEAGAIN(t *testing.T) {
	r, w := mkPair(t)
	_ = w
	if err := syscall.SetNonblock(r, true); err != nil {
		t.Fatal(err)
	}
	_, err := recvLoop(r, 1, RTM_NEWLINK)
	if err == nil {
		t.Fatal("空非阻塞 socket 应返回错误")
	}
	if !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("应返回 EAGAIN，实得 %v", err)
	}
}

// TestRecvLoopAck 关键回归：单条 ACK 视为请求完成（否则命令超时 EAGAIN）。
// 这是 v0.5.2 后 veth 仍会失败的潜在根因。
func TestRecvLoopAck(t *testing.T) {
	r, w := mkPair(t)
	if _, err := syscall.Write(w, ackMsg(9)); err != nil {
		t.Fatal(err)
	}
	out, err := recvLoop(r, 9, RTM_NEWLINK)
	if err != nil {
		t.Fatalf("单 ACK 应立即成功，实得 %v", err)
	}
	_ = out
}

// TestRecvLoopOpError 错误注入：NLMSG_ERROR(code=-EPERM)。
func TestRecvLoopOpError(t *testing.T) {
	r, w := mkPair(t)
	if _, err := syscall.Write(w, errMsg(5, -int32(syscall.EPERM))); err != nil {
		t.Fatal(err)
	}
	_, err := recvLoop(r, 5, RTM_NEWLINK)
	if err == nil {
		t.Fatal("应返回 OpError")
	}
	var oe *OpError
	if !errors.As(err, &oe) {
		t.Fatalf("应为 *OpError，实得 %T", err)
	}
	if !errors.Is(oe, syscall.EPERM) {
		t.Fatalf("应包装 EPERM，实得 %v", oe)
	}
}

// TestRecvLoopSeqMismatch 验证不匹配 seq 的应答被跳过、匹配的才生效。
func TestRecvLoopSeqMismatch(t *testing.T) {
	r, w := mkPair(t)
	if _, err := syscall.Write(w, ackMsg(999)); err != nil { // 别人的 ACK
		t.Fatal(err)
	}
	if _, err := syscall.Write(w, ackMsg(3)); err != nil { // 我们的 ACK
		t.Fatal(err)
	}
	if _, err := recvLoop(r, 3, RTM_NEWLINK); err != nil {
		t.Fatalf("应跳过异 seq 后成功，实得 %v", err)
	}
}
