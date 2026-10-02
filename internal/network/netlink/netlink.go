// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

// Package netlink 是 Boxli 自研的最小 rtnetlink 客户端：只实现网络模块
// 需要的那部分消息（链路增删改查、地址增删查、路由增删查、命名空间切换），
// 直接与内核 NETLINK_ROUTE 套接字对话。
//
// 之所以自研而不引入 vishvananda/netlink 之类的库：AGENTS.md 规定容器 /
// 网络相关第三方库一律不批，且这些库体积远超本模块所需。本包只依赖标准库
// syscall，无 CGO。
package netlink

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// 本包使用的 rtnetlink 常量（syscall 包未导出这些值）。
const (
	// 消息类型。
	NLmsgNoop  = 0x1
	NLmsgError = 0x2
	NLmsgDone  = 0x3

	// 通用标志。
	NLM_F_REQUEST = 0x1
	NLM_F_MULTI   = 0x2
	NLM_F_ACK     = 0x4
	NLM_F_ECHO    = 0x8
	NLM_F_DUMP    = 0x300 // NLM_F_ROOT|NLM_F_MATCH

	NLM_F_REPLACE = 0x100
	NLM_F_EXCL    = 0x200
	NLM_F_CREATE  = 0x400
	NLM_F_APPEND  = 0x800

	// 路由消息族。
	RTM_NEWLINK  = 16
	RTM_DELLINK  = 17
	RTM_GETLINK  = 18
	RTM_NEWADDR  = 20
	RTM_DELADDR  = 21
	RTM_GETADDR  = 22
	RTM_NEWROUTE = 24
	RTM_DELROUTE = 25
	RTM_GETROUTE = 26

	// 地址族。
	AF_UNSPEC = 0
	AF_INET   = 2
	AF_INET6  = 10

	// 链路属性。
	IFLA_ADDRESS    = 1
	IFLA_IFNAME     = 3
	IFLA_MTU        = 4
	IFLA_MASTER     = 10
	IFLA_LINKINFO   = 18
	IFLA_NET_NS_PID = 19
	IFLA_NET_NS_FD  = 28

	IFLA_INFO_KIND = 1
	IFLA_INFO_DATA = 2 // IFLA_LINKINFO 内的数据（与 IFA_* 数值重叠但属不同命名空间）

	// 地址属性。
	IFA_ADDRESS = 1
	IFA_LOCAL   = 2

	// 路由属性。
	RTA_DST      = 1
	RTA_OIF      = 4
	RTA_GATEWAY  = 5
	RTA_PRIORITY = 6
	RTA_TABLE    = 15

	// 路由表与协议。
	RT_TABLE_MAIN     = 254
	RTPROT_BOOT       = 3
	RT_SCOPE_UNIVERSE = 0
	RT_SCOPE_LINK     = 253
	RTN_UNICAST       = 1

	// 链路类型标记（ifi_type）。
	ARPHRD_ETHER    = 1
	ARPHRD_LOOPBACK = 772

	// veth 私有属性（IFLA_INFO_DATA 内）。
	VETH_INFO_PEER = 1

	// 命名空间类型（setns）。
	CLONE_NEWNET = 0x40000000
)

// sysSettid 用于把 goroutine 锁到 OS 线程上做 setns（命名空间是线程级的）。
var sysSettid = syscall.Gettid

// 单例套接字：rtnetlink 请求串行化，避免多 goroutine 交叉读写。
var (
	sockOnce sync.Once
	sockFD   int
	sockErr  error
	sockMu   sync.Mutex

	// socketOverride 测试注入：返回 (fd, err, 使用注入)。一次返回 ok=true 即优先
	// 于真实套接字；返回 false 则回落到默认。仅测试设置，非并发安全。
	socketOverride func() (int, error, bool)
)

// socket 返回常驻的 NETLINK_ROUTE 套接字。套接字不绑定到特定网络命名空间，
// 每次请求时由调用方通过 Setns 决定当前线程所在的命名空间。
// 测试可用 socketOverride 注入伪套接字（伪造 EAGAIN/EPERM 等，无需 root）。
func socket() (int, error) {
	if socketOverride != nil {
		fd, err, ok := socketOverride()
		if ok {
			return fd, err
		}
	}
	sockOnce.Do(func() {
		fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
		if err != nil {
			sockErr = fmt.Errorf("创建 rtnetlink 套接字: %w", err)
			return
		}
		// 只订阅内核发来的 ACK，避免被组播事件淹没。
		sa := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
		if err := syscall.Bind(fd, sa); err != nil {
			_ = syscall.Close(fd)
			sockErr = fmt.Errorf("绑定 rtnetlink 套接字: %w", err)
			return
		}
		// 接收超时：若内核因命名空间/状态异常不回应，避免 Recvfrom 永久阻塞
		// 卡死调用方（曾表现为 stop/rm/netlink 一连串操作挂起、Ctrl+C 无效）。
		tv := syscall.Timeval{Sec: 5}
		if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
			_ = syscall.Close(fd)
			sockErr = fmt.Errorf("设置 rtnetlink 接收超时: %w", err)
			return
		}
		sockFD = fd
	})
	return sockFD, sockErr
}

// nlmsg 是 rtnetlink 消息头。
type nlmsg struct {
	Len   uint32
	Type  uint16
	Flags uint16
	Seq   uint32
	Pid   uint32
}

// attr 是 rtnetlink 属性头（含内核侧对齐补齐）。
type attr struct {
	Len  uint16
	Type uint16
}

// attrAlign 按 NLA_ALIGNTO=4 向上取整。
func attrAlign(n int) int { return (n + 3) &^ 3 }

// req 组装一条请求消息：头部 + 负载 + 属性。
type req struct {
	buf []byte
	seq uint32
}

var seqCounter uint32
var seqMu sync.Mutex

func nextSeq() uint32 {
	seqMu.Lock()
	defer seqMu.Unlock()
	seqCounter++
	return seqCounter
}

func newReq(msgType uint16, flags uint16, payload []byte) *req {
	r := &req{seq: nextSeq()}
	r.buf = make([]byte, 16, 16+len(payload)+64)
	putU32(r.buf[0:4], uint32(16+len(payload)))
	putU16(r.buf[4:6], msgType)
	putU16(r.buf[6:8], flags|NLM_F_REQUEST|NLM_F_ACK)
	putU32(r.buf[8:12], r.seq)
	putU32(r.buf[12:16], 0) // pid=0：内核自填
	r.buf = append(r.buf, payload...)
	return r
}

// addAttr 追加一个属性，长度含头部并按 4 字节对齐补齐。
func (r *req) addAttr(typ uint16, data []byte) {
	n := attrAlign(4 + len(data))
	start := len(r.buf)
	r.buf = append(r.buf, make([]byte, n)...)
	putU16(r.buf[start:start+2], uint16(4+len(data)))
	putU16(r.buf[start+2:start+4], typ)
	copy(r.buf[start+4:], data)
	// 修正消息总长。
	putU32(r.buf[0:4], uint32(len(r.buf)))
}

// addAttrString 追加 NUL 结尾的字符串属性。
func (r *req) addAttrString(typ uint16, s string) {
	r.addAttr(typ, append([]byte(s), 0))
}

// addAttrU32 追加 32 位整数属性。
func (r *req) addAttrU32(typ uint16, v uint32) {
	b := make([]byte, 4)
	putU32(b, v)
	r.addAttr(typ, b)
}

// addAttrU16 追加 16 位整数属性（内核按 4 字节对齐存放）。
func (r *req) addAttrU16(typ uint16, v uint16) {
	b := make([]byte, 4)
	putU16(b, v)
	r.addAttr(typ, b)
}

// do 发送请求并收集响应，返回全部消息负载（不含消息头）。
// 遇到 NLMSG_ERROR 且 error!=0 时返回 *OpError。
func (r *req) do() ([]message, error) {
	fd, err := socket()
	if err != nil {
		return nil, err
	}
	sockMu.Lock()
	defer sockMu.Unlock()

	if err := syscall.Sendto(fd, r.buf, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("发送 rtnetlink 请求(type=%d): %w", msgTypeOf(r.buf), err)
	}
	return recvLoop(fd, r.seq, msgTypeOf(r.buf))
}

// recvLoop 从 netlink 套接字接收并解析 seq 匹配的应答，直到完成或出错。
// 抽出以便错误注入测试（给非阻塞空 socket，得到 EAGAIN）无需 root。
func recvLoop(fd int, seq uint32, msgType uint16) ([]message, error) {
	var out []message
	buf := make([]byte, 1<<16)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				// SO_RCVTIMEO 到期，内核未在窗口内回 ACK/响应。这通常是
				// 请求构造被内核丢弃（如 veth 嵌套属性错误），或命名空间状态
				// 异常。返回明确错误，避免调用方误以为成功。
				return nil, fmt.Errorf("rtnetlink %s 等待应答超时（内核未确认请求，可能请求构造错误或接口状态异常）: %w",
					opName(msgType), err)
			}
			return nil, fmt.Errorf("接收 rtnetlink 响应: %w", err)
		}
		msgs, err := parseMessages(buf[:n], seq)
		if err != nil {
			return nil, err
		}
		done := false
		for _, m := range msgs {
			switch m.Type {
			case NLmsgError:
				if len(m.Data) < 4 {
					return nil, fmt.Errorf("rtnetlink 错误消息长度不足: %w", ErrShortMessage)
				}
				code := int32(getU32(m.Data[0:4]))
				if code == 0 {
					// ACK：命令已被内核接受并确认，作为本次请求的终止条件返回。
					// （此前在此 continue 会导致对端只回单个 ACK 的命令——如
					// AddAddr/NewLink/NewVeth——永远收不到终止信号，最终超时
					// EAGAIN 而误判失败。）
					return out, nil
				}
				return nil, &OpError{Op: opName(msgType), Errno: syscall.Errno(-code)}
			case NLmsgDone:
				done = true
			case NLmsgNoop:
			default:
				out = append(out, m)
				if m.Flags&NLM_F_MULTI == 0 {
					done = true
				}
			}
		}
		if done {
			return out, nil
		}
	}
}

// message 是一条解析后的 rtnetlink 消息。
type message struct {
	Type  uint16
	Flags uint16
	Data  []byte
}

// ErrShortMessage 表示内核返回的消息被截断（防御性检查）。
var ErrShortMessage = errors.New("boxli/network/netlink: 消息长度不足")

// OpError 包装一次 rtnetlink 操作的 errno。
type OpError struct {
	// Op 是操作名，如 "add-link"。
	Op string
	// Errno 是内核返回的错误码。
	Errno syscall.Errno
}

func (e *OpError) Error() string {
	return fmt.Sprintf("rtnetlink %s: %v", e.Op, e.Errno)
}

// Unwrap 让 errors.Is(err, syscall.EPERM) 等工作。
func (e *OpError) Unwrap() error { return e.Errno }

func opName(msgType uint16) string {
	switch msgType {
	case RTM_NEWLINK:
		return "add-link"
	case RTM_DELLINK:
		return "del-link"
	case RTM_GETLINK:
		return "get-link"
	case RTM_NEWADDR:
		return "add-addr"
	case RTM_DELADDR:
		return "del-addr"
	case RTM_GETADDR:
		return "get-addr"
	case RTM_NEWROUTE:
		return "add-route"
	case RTM_DELROUTE:
		return "del-route"
	case RTM_GETROUTE:
		return "get-route"
	}
	return fmt.Sprintf("msg-%d", msgType)
}

func msgTypeOf(buf []byte) uint16 {
	if len(buf) < 6 {
		return 0
	}
	return getU16(buf[4:6])
}

func parseMessages(b []byte, seq uint32) ([]message, error) {
	var out []message
	for len(b) >= 16 {
		l := int(getU32(b[0:4]))
		if l < 16 || l > len(b) {
			return nil, fmt.Errorf("rtnetlink 消息长度 %d 非法: %w", l, ErrShortMessage)
		}
		if s := getU32(b[8:12]); s != seq {
			// 其他请求的残留消息：跳过。
			b = b[attrAlign(l):]
			continue
		}
		out = append(out, message{
			Type:  getU16(b[4:6]),
			Flags: getU16(b[6:8]),
			Data:  b[16:l],
		})
		b = b[attrAlign(l):]
	}
	return out, nil
}

// parseAttrs 解析消息负载里的属性表（负载前 16 字节是 struct ifinfomsg 等固定头）。
func parseAttrs(b []byte) (map[uint16][]byte, error) {
	out := map[uint16][]byte{}
	for len(b) >= 4 {
		l := int(getU16(b[0:2]))
		t := getU16(b[2:4])
		if l < 4 || l > len(b) {
			return nil, fmt.Errorf("rtnetlink 属性长度 %d 非法: %w", l, ErrShortMessage)
		}
		out[t] = b[4:l]
		b = b[attrAlign(l):]
	}
	return out, nil
}

// parseNested 解析嵌套属性（IFLA_LINKINFO 之类），返回其内部属性表。
func parseNested(b []byte) (map[uint16][]byte, error) { return parseAttrs(b) }

func putU16(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
func getU16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
func getU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// ThreadID 返回当前 OS 线程 ID（netlink 命名空间切换需要锁线程）。
func ThreadID() int { return sysSettid() }

var _ = os.Getpid
