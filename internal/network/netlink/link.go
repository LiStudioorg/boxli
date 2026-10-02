// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package netlink

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
)

// ErrLinkNotFound 表示按名字找不到链路。
var ErrLinkNotFound = errors.New("boxli/network/netlink: 链路不存在")

// 常用链路类型 kind 值。
const (
	KindBridge = "bridge"
	KindVeth   = "veth"
)

// Link 是内核里的一个网络接口（ifindex + 名字）。
type Link struct {
	IfIndex int
	Name    string
	MTU     int
	Flags   uint32
}

// ifInfoMsg 生成 ifinfomsg 固定头（16 字节）。
func ifInfoMsg() []byte {
	b := make([]byte, 16)
	b[0] = syscall.AF_UNSPEC
	return b
}

// ifInfoMsgIdx 生成 ifinfomsg 固定头并带上 ifindex。
func ifInfoMsgIdx(ifindex uint32) []byte {
	b := ifInfoMsg()
	binary.LittleEndian.PutUint32(b[4:8], ifindex)
	return b
}

// NewLink 在当前命名空间创建 kind 类型的链路。optsExtra 是额外属性。
func NewLink(name, kind string, optsExtra ...Attr) error {
	r := newReq(RTM_NEWLINK, NLM_F_CREATE|NLM_F_EXCL, ifInfoMsg())
	r.addAttrString(IFLA_IFNAME, name)
	if kind != "" {
		r.addAttr(IFLA_LINKINFO, nestedAttrs([]Attr{
			{Type: IFLA_INFO_KIND, Data: cstr(kind)},
		}))
	}
	for _, a := range optsExtra {
		r.addAttr(a.Type, a.Data)
	}
	_, err := r.do()
	return err
}

// NewVeth 创建一对 veth：name + peer。
func NewVeth(name, peer string) error {
	peerHdr := append(ifInfoMsg(), cstr(peer)...)
	r := newReq(RTM_NEWLINK, NLM_F_CREATE|NLM_F_EXCL, ifInfoMsg())
	r.addAttrString(IFLA_IFNAME, name)
	r.addAttr(IFLA_LINKINFO, nestedAttrs([]Attr{
		{Type: IFLA_INFO_KIND, Data: cstr(KindVeth)},
		{Type: 2, Data: nestedAttrs([]Attr{ // IFLA_INFO_DATA=2
			{Type: 1, Data: peerHdr}, // VETH_INFO_PEER=1
		})},
	}))
	_, err := r.do()
	return err
}

// DelLink 按名字删除链路。
func DelLink(name string) error {
	r := newReq(RTM_DELLINK, 0, ifInfoMsg())
	r.addAttrString(IFLA_IFNAME, name)
	_, err := r.do()
	return err
}

// LinkByName 按名字查找链路（当前命名空间）。
func LinkByName(name string) (*Link, error) {
	r := newReq(RTM_GETLINK, 0, ifInfoMsg())
	r.addAttrString(IFLA_IFNAME, name)
	msgs, err := r.do()
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if m.Type != RTM_NEWLINK || len(m.Data) < 16 {
			continue
		}
		attrs, err := parseAttrs(m.Data[16:])
		if err != nil {
			continue
		}
		if ifName := trimAttrString(attrBytes(attrs, IFLA_IFNAME)); ifName != name {
			continue
		}
		return &Link{
			IfIndex: int(binary.LittleEndian.Uint32(m.Data[4:8])),
			Name:    name,
			MTU:     int(binary.LittleEndian.Uint32(attrBytes(attrs, IFLA_MTU))),
			Flags:   binary.LittleEndian.Uint32(m.Data[8:12]),
		}, nil
	}
	return nil, fmt.Errorf("链路 %s: %w", name, ErrLinkNotFound)
}

// trimAttrString 去掉内核字符串属性末尾的 NUL/对齐填充（IFLA_IFNAME 等是
// NUL 结尾，attrBytes 原样返回含填充字节）；不足 3 字节（如 u32 属性）原样返回。
func trimAttrString(b []byte) string {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0 {
			return string(b[:i+1])
		}
	}
	return string(b)
}

// LinkUp / LinkDown 切换接口上下线。
func LinkUp(name string) error {
	r := newReq(RTM_NEWLINK, 0, ifInfoMsgIdx(mustIndex(name)))
	r.addAttrString(IFLA_IFNAME, name)
	r.buf = setIFFBuf(r.buf, syscall.IFF_UP, syscall.IFF_UP)
	_, err := r.do()
	return err
}

func LinkDown(name string) error {
	r := newReq(RTM_NEWLINK, 0, ifInfoMsgIdx(mustIndex(name)))
	r.addAttrString(IFLA_IFNAME, name)
	r.buf = setIFFBuf(r.buf, 0, syscall.IFF_UP)
	_, err := r.do()
	return err
}

func mustIndex(name string) uint32 {
	l, err := LinkByName(name)
	if err != nil {
		return 0
	}
	return uint32(l.IfIndex)
}

// setIFFBuf 修正 ifinfomsg 头里的 flags 与 change 字段（offset 8 与 12）。
func setIFFBuf(buf []byte, flags, change uint32) []byte {
	binary.LittleEndian.PutUint32(buf[8:12], flags)
	binary.LittleEndian.PutUint32(buf[12:16], change)
	return buf
}

// SetLinkMaster 把链路挂到指定主链路（网桥）下。
func SetLinkMaster(name, master string) error {
	l, err := LinkByName(master)
	if err != nil {
		return err
	}
	r := newReq(RTM_NEWLINK, 0, ifInfoMsgIdx(uint32(l.IfIndex)))
	r.addAttrString(IFLA_IFNAME, name)
	r.addAttr(IFLA_MASTER, u32(uint32(l.IfIndex)))
	_, err = r.do()
	return err
}

// SetLinkNetnsPid 把链路移动到 pid 的网络命名空间。
func SetLinkNetnsPid(name string, pid int) error {
	r := newReq(RTM_NEWLINK, 0, ifInfoMsg())
	r.addAttrString(IFLA_IFNAME, name)
	r.addAttr(IFLA_NET_NS_PID, u32(uint32(pid)))
	_, err := r.do()
	return err
}

// AddAddr 为接口绑定一个 IP。ip 如 "172.18.0.2"，prefix 如 16。
func AddAddr(ifname, ip string, prefix int) error {
	idx := mustIndex(ifname)
	b := make([]byte, 8)
	b[0] = syscall.AF_INET
	b[2] = byte(prefix)
	binary.LittleEndian.PutUint32(b[4:8], idx)
	r := newReq(RTM_NEWADDR, NLM_F_CREATE|NLM_F_EXCL, b)
	r.addAttr(IFA_LOCAL, netIP4(ip))
	r.addAttr(IFA_ADDRESS, netIP4(ip))
	_, err := r.do()
	return err
}

// DelAddr 移除接口上的 IP。
func DelAddr(ifname, ip string, prefix int) error {
	idx := mustIndex(ifname)
	b := make([]byte, 8)
	b[0] = syscall.AF_INET
	b[2] = byte(prefix)
	binary.LittleEndian.PutUint32(b[4:8], idx)
	r := newReq(RTM_DELADDR, 0, b)
	r.addAttr(IFA_LOCAL, netIP4(ip))
	r.addAttr(IFA_ADDRESS, netIP4(ip))
	_, err := r.do()
	return err
}

// AddRoute 添加一条 IPv4 路由。dst 为空表示默认路由。
func AddRoute(dst string, prefix int, via string, ifname string) error {
	b := make([]byte, 12)
	b[0] = syscall.AF_INET
	if dst == "" {
		prefix = 0
	}
	b[1] = byte(prefix)
	binary.LittleEndian.PutUint32(b[4:8], RT_TABLE_MAIN)
	binary.LittleEndian.PutUint32(b[8:12], RTN_UNICAST)
	r := newReq(RTM_NEWROUTE, NLM_F_CREATE|NLM_F_EXCL, b)
	if dst != "" {
		r.addAttr(RTA_DST, netIP4(dst))
	}
	if via != "" {
		r.addAttr(RTA_GATEWAY, netIP4(via))
	}
	if ifname != "" {
		if l, err := LinkByName(ifname); err == nil {
			r.addAttr(RTA_OIF, u32(uint32(l.IfIndex)))
		}
	}
	_, err := r.do()
	return err
}

// DelRoute 删除一条 IPv4 路由。
func DelRoute(dst string, prefix int, via string, ifname string) error {
	b := make([]byte, 12)
	b[0] = syscall.AF_INET
	if dst == "" {
		prefix = 0
	}
	b[1] = byte(prefix)
	binary.LittleEndian.PutUint32(b[4:8], RT_TABLE_MAIN)
	binary.LittleEndian.PutUint32(b[8:12], RTN_UNICAST)
	r := newReq(RTM_DELROUTE, 0, b)
	if dst != "" {
		r.addAttr(RTA_DST, netIP4(dst))
	}
	if via != "" {
		r.addAttr(RTA_GATEWAY, netIP4(via))
	}
	if ifname != "" {
		if l, err := LinkByName(ifname); err == nil {
			r.addAttr(RTA_OIF, u32(uint32(l.IfIndex)))
		}
	}
	_, err := r.do()
	return err
}

// ---- 辅助 ----

// Attr 是用户侧表达的一个 rtnetlink 属性。
type Attr struct {
	Type uint16
	Data []byte
}

// nestedAttrs 把一组属性编码为嵌套属性体。
func nestedAttrs(attrs []Attr) []byte {
	var buf []byte
	for _, a := range attrs {
		n := attrAlign(4 + len(a.Data))
		out := make([]byte, n)
		putU16(out[0:2], uint16(4+len(a.Data)))
		putU16(out[2:4], a.Type)
		copy(out[4:], a.Data)
		buf = append(buf, out...)
	}
	return buf
}

// cstr 转为 NUL 结尾字符串字节。
func cstr(s string) []byte { return append([]byte(s), 0) }

// u32 编码 32 位小端。
func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// netIP4 把 "172.18.0.2" 编码为 4 字节；格式错误返回 nil。
func netIP4(s string) []byte {
	out := [4]byte{}
	var cur byte
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if n >= 3 {
				return nil
			}
			out[n] = cur
			n++
			cur = 0
			continue
		}
		if c < '0' || c > '9' {
			return nil
		}
		cur = cur*10 + (c - '0')
	}
	if n != 3 {
		return nil
	}
	out[3] = cur
	return out[:]
}

// attrBytes 从 parseAttrs 结果里取属性原始字节。
func attrBytes(attrs map[uint16][]byte, t uint16) []byte {
	v, ok := attrs[t]
	if !ok {
		return nil
	}
	return v
}

// MustIndexOf 返回接口 ifindex。
func MustIndexOf(name string) (uint32, error) {
	l, err := LinkByName(name)
	if err != nil {
		return 0, err
	}
	return uint32(l.IfIndex), nil
}
