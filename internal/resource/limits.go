// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package resource

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// 本文件实现资源参数的解析与校验（跨平台，无 build tag），是 CLI 与
// cgroups 写入之间的唯一"事实来源"：CLI 只负责把字符串交进来，一切单位
// 换算、范围检查、互斥关系都在这里完成。

// CgroupV2Mount 是 cgroups v2 统一层级的挂载点。
const CgroupV2Mount = "/sys/fs/cgroup"

// LiCoreGroup 是 LiCore 在 cgroup 根下自建的一级组名：所有容器共享一个父组，
// 便于 `licore rm` 后整体回收，也避免污染宿主其它 cgroup。
const LiCoreGroup = "licore"

// 内存与 IO 的默认约束（与 Linux 惯用值保持一致）。
const (
	// DefaultCPUPeriod 是 CFS 配额周期（微秒），cgroups v2 cpu.max 的默认值。
	DefaultCPUPeriod = 100000
	// MinBlkioWeight / MaxBlkioWeight 是 --blkio-weight 的合法区间
	// （与 Docker 一致，对应 cgroup v1 blkio.weight 语义）。
	MinBlkioWeight = 10
	MaxBlkioWeight = 1000
	// DefaultBlkioWeight 是未指定 --blkio-weight 时单设备的默认权重。
	DefaultBlkioWeight = 500
	// DefaultMemoryReservation 是未指定 --memory-reservation 时按内存上限
	// 推出的软限制比例（Docker 的默认行为）。
	DefaultMemoryReservation = 0.5
)

// 哨兵错误：调用方用 errors.Is 判断，文案留给上层包装。
var (
	// ErrUnsupported 表示当前环境不具备该能力的承载条件（例如没有 cgroups v2、
	// 宿主未挂载 XFS project quota）。调用方应降级并提示，而不是让容器启动失败。
	ErrUnsupported = errors.New("licore/resource: 当前环境不支持该资源能力")
	// ErrBadParam 表示用户给的资源参数本身非法（单位错误、越界、互相矛盾）。
	ErrBadParam = errors.New("licore/resource: 资源参数非法")
	// ErrBadDevice 表示设备标识非法（PCI 地址、/dev 路径或 major:minor）。
	ErrBadDevice = errors.New("licore/resource: 设备标识非法")
)

// Limits 是一次容器运行的全部资源限制请求。零值表示"全部不限制"，
// 每个字段对应一个 CLI 参数，单位在注释里写明（一律换算成内核原始单位）。
type Limits struct {
	// CPUs 是 `--cpus`：CPU 核数，会换算成 cpu.max 的 quota（period 固定
	// DefaultCPUPeriod）。0 表示不限制（写 "max"）。
	CPUs float64
	// CPUShares 是 `--cpu-shares`：相对权重，区间 [2,262144]（cgroup v1
	// cpu.shares 语义），落到 v2 的 cpu.weight（区间 [1,10000]）。
	CPUShares int64
	// CPUSet 是 `--cpuset-cpus`：允许使用的 CPU 列表，形如 "0-3,7"。
	CPUSet string
	// Memory 是 `--memory`：硬上限（字节）。0 表示不限制。
	Memory int64
	// MemorySwap 是 `--memory-swap`：内存 + swap 的总上限（字节）。
	// 0 表示不限制；-1 表示"内存上限 + 不限 swap"；其它负值非法。
	MemorySwap int64
	// MemoryReservation 是 `--memory-reservation`：软限制（字节），落到
	// memory.low。0 表示不限制。
	MemoryReservation int64
	// OOMKillDisable 是 `--oom-kill-disable`：关闭该 cgroup 的 OOM killer
	// （写 memory.oom.group=0 与禁用 oom 击杀，见 cgroup_linux.go）。
	OOMKillDisable bool
	// PidsLimit 是 `--pids-limit`：进程/线程数上限。0 表示不限制。
	PidsLimit int64
	// BlkioWeight 是 `--blkio-weight`：块设备相对权重，区间 [10,1000]。
	BlkioWeight int64
	// BlkioWeightDevice 是 `--blkio-weight-device`：单设备权重覆盖。
	BlkioWeightDevice []WeightDevice
	// DeviceReadBps 是 `--device-read-bps`：单设备读带宽上限（字节/秒）。
	DeviceReadBps []ThrottleDevice
	// DeviceWriteBps 是 `--device-write-bps`：单设备写带宽上限（字节/秒）。
	DeviceWriteBps []ThrottleDevice
	// DeviceReadIOps 是 `--device-read-iops`：单设备读 IOPS 上限。
	DeviceReadIOps []ThrottleDevice
	// DeviceWriteIOps 是 `--device-write-iops`：单设备写 IOPS 上限。
	DeviceWriteIOps []ThrottleDevice
	// Storage 是 `--storage`：可写层存储配额（字节）。0 表示不限制。
	Storage int64
	// GPU 是 `--gpu`：需要直通的加速器请求（NVIDIA / 通用 DRM）。
	GPU []DeviceRequest
	// NPU 是 `--npu`：需要直通的手机 NPU 请求（Qualcomm kgsl、Hexagon DSP 等）。
	NPU []DeviceRequest
	// NetworkBandwidth 是 `--network-bandwidth`：容器出向带宽上限（字节/秒）。
	NetworkBandwidth int64
}

// WeightDevice 是单块设备的 blkio 权重（cgroup v1 语义，v2 由本包换算）。
type WeightDevice struct {
	// Device 是设备标识：/dev/sda 这类路径或 major:minor。
	Device string
	// Weight 是权重，区间 [MinBlkioWeight, MaxBlkioWeight]。
	Weight int64
}

// ThrottleDevice 是单块设备的 IO 限速项。
type ThrottleDevice struct {
	// Device 是设备标识：/dev/sda 这类路径或 major:minor。
	Device string
	// Rate 是每秒上限：Bps 类为字节/秒，IOps 类为次数/秒。
	Rate int64
}

// CPUQuota 返回 cpu.max 需要写入的 quota（微秒）；CPUs<=0 时返回 -1
// 表示 "max"（不限制）。
func (l *Limits) CPUQuota() int64 {
	if l == nil || l.CPUs <= 0 {
		return -1
	}
	q := int64(l.CPUs * float64(DefaultCPUPeriod))
	if q < 1000 {
		// 内核要求 quota >= 1000us；0.001 核以下没有实际意义，钳到下限。
		q = 1000
	}
	return q
}

// CPUWeight 返回 cpu.weight 需要写入的值（1..10000）；CPUShares<=0 时
// 返回 -1 表示不写入（沿用内核默认 100）。
func (l *Limits) CPUWeight() int64 {
	if l == nil || l.CPUShares <= 0 {
		return -1
	}
	return SharesToWeight(l.CPUShares)
}

// SwapLimit 返回 memory.swap.max 需要写入的"swap 单独上限"（字节），
// 与 ConvertMemorySwap 的语义一致：-1 表示 "max"（无上限），
// 0 表示不写入（沿用宿主默认），其余为具体字节数。
func (l *Limits) SwapLimit() int64 {
	if l == nil {
		return 0
	}
	v, err := ConvertMemorySwap(l.MemorySwap, l.Memory)
	if err != nil {
		return 0
	}
	return v
}

// Validate 校验全部参数；任何非法组合都返回包装了 ErrBadParam 的错误，
// 便于 CLI 在真正建 cgroup 之前就把问题挡下来。
func (l *Limits) Validate() error {
	if l == nil {
		return nil
	}
	if math.IsNaN(l.CPUs) || math.IsInf(l.CPUs, 0) || l.CPUs < 0 {
		return fmt.Errorf("--cpus=%v 非法：必须是 >= 0 的有限数: %w", l.CPUs, ErrBadParam)
	}
	if l.CPUs > 0 && l.CPUs*float64(DefaultCPUPeriod) < 1000 {
		return fmt.Errorf("--cpus=%v 过小：内核要求 quota >= 1000us（最小 %v 核）: %w",
			l.CPUs, 1000.0/float64(DefaultCPUPeriod), ErrBadParam)
	}
	if l.CPUShares < 0 || (l.CPUShares > 0 && (l.CPUShares < 2 || l.CPUShares > 262144)) {
		return fmt.Errorf("--cpu-shares=%d 越界：合法区间 [2,262144]: %w", l.CPUShares, ErrBadParam)
	}
	if l.Memory < 0 {
		return fmt.Errorf("--memory=%d 非法：不能为负（0=不限制）: %w", l.Memory, ErrBadParam)
	}
	if l.MemoryReservation < 0 {
		return fmt.Errorf("--memory-reservation=%d 非法：不能为负（0=不限制）: %w", l.MemoryReservation, ErrBadParam)
	}
	if l.Memory > 0 && l.MemoryReservation > l.Memory {
		return fmt.Errorf("--memory-reservation=%d 不能大于 --memory=%d: %w", l.MemoryReservation, l.Memory, ErrBadParam)
	}
	if l.MemorySwap < -1 {
		return fmt.Errorf("--memory-swap=%d 非法：只允许 -1（不限 swap）或 >= 0 的字节数: %w", l.MemorySwap, ErrBadParam)
	}
	if _, err := ConvertMemorySwap(l.MemorySwap, l.Memory); err != nil {
		return err
	}
	if l.OOMKillDisable && l.Memory == 0 && l.MemorySwap == 0 {
		// 内核层面 memory.oom.group 关掉 OOM 击杀需要先有内存约束，
		// 否则等于向内核请求"永不回收"，属误用。
		return fmt.Errorf("--oom-kill-disable 必须与 --memory 或 --memory-swap 同时使用: %w", ErrBadParam)
	}
	if l.PidsLimit < 0 {
		return fmt.Errorf("--pids-limit=%d 非法：不能为负（0=不限制）: %w", l.PidsLimit, ErrBadParam)
	}
	if l.BlkioWeight != 0 && (l.BlkioWeight < MinBlkioWeight || l.BlkioWeight > MaxBlkioWeight) {
		return fmt.Errorf("--blkio-weight=%d 越界：合法区间 [%d,%d]: %w",
			l.BlkioWeight, MinBlkioWeight, MaxBlkioWeight, ErrBadParam)
	}
	for _, wd := range l.BlkioWeightDevice {
		if _, err := ParseDeviceID(wd.Device); err != nil {
			return err
		}
		if wd.Weight < MinBlkioWeight || wd.Weight > MaxBlkioWeight {
			return fmt.Errorf("--blkio-weight-device 权重 %d 越界：合法区间 [%d,%d]: %w",
				wd.Weight, MinBlkioWeight, MaxBlkioWeight, ErrBadParam)
		}
	}
	for _, list := range [][]ThrottleDevice{l.DeviceReadBps, l.DeviceWriteBps, l.DeviceReadIOps, l.DeviceWriteIOps} {
		for _, td := range list {
			if _, err := ParseDeviceID(td.Device); err != nil {
				return err
			}
			if td.Rate <= 0 {
				return fmt.Errorf("设备 %s 的限速值必须是正数，得到 %d: %w", td.Device, td.Rate, ErrBadParam)
			}
		}
	}
	if l.Storage < 0 {
		return fmt.Errorf("--storage=%d 非法：不能为负（0=不限制）: %w", l.Storage, ErrBadParam)
	}
	if l.NetworkBandwidth < 0 {
		return fmt.Errorf("--network-bandwidth=%d 非法：不能为负（0=不限制）: %w", l.NetworkBandwidth, ErrBadParam)
	}
	for _, r := range append(append([]DeviceRequest{}, l.GPU...), l.NPU...) {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Empty 报告是否没有设置任何限制（用于跳过建 cgroup 与全部写入）。
func (l *Limits) Empty() bool {
	if l == nil {
		return true
	}
	return l.CPUs == 0 && l.CPUShares == 0 && l.CPUSet == "" &&
		l.Memory == 0 && l.MemorySwap == 0 && l.MemoryReservation == 0 && !l.OOMKillDisable &&
		l.PidsLimit == 0 && l.BlkioWeight == 0 && len(l.BlkioWeightDevice) == 0 &&
		len(l.DeviceReadBps) == 0 && len(l.DeviceWriteBps) == 0 &&
		len(l.DeviceReadIOps) == 0 && len(l.DeviceWriteIOps) == 0 &&
		l.Storage == 0 && len(l.GPU) == 0 && len(l.NPU) == 0 && l.NetworkBandwidth == 0
}

// ConvertMemorySwap 把 `--memory-swap` 的 v1 语义换算成 cgroups v2
// memory.swap.max 的"swap 单独上限"，与 Docker / runc 的算法一致：
//
//	memorySwap == 0  → 0（未指定，沿用宿主默认）
//	memorySwap == -1 → -1（swap 不限制）
//	memorySwap > 0   → memorySwap - memory（必须 >= memory）
func ConvertMemorySwap(memorySwap, memory int64) (int64, error) {
	switch {
	case memorySwap == 0:
		return 0, nil
	case memorySwap == -1:
		return -1, nil
	case memorySwap < 0:
		return 0, fmt.Errorf("--memory-swap=%d 非法：只允许 -1 或非负字节数: %w", memorySwap, ErrBadParam)
	case memory == 0:
		return 0, fmt.Errorf("--memory-swap=%d 需要同时指定 --memory（否则无从换算 swap 上限）: %w", memorySwap, ErrBadParam)
	case memorySwap < memory:
		return 0, fmt.Errorf("--memory-swap=%d 不能小于 --memory=%d: %w", memorySwap, memory, ErrBadParam)
	}
	return memorySwap - memory, nil
}

// SharesToWeight 把 cgroup v1 的 cpu.shares（[2,262144]）换算成 cgroups v2
// 的 cpu.weight（[1,10000]），与 Docker / runc 的公式一致：
// weight = 1 + (shares-2)*9999/262142。shares 非正时返回 100（内核默认）。
func SharesToWeight(shares int64) int64 {
	if shares <= 0 {
		return 100
	}
	return 1 + (shares-2)*9999/262142
}

// WeightToShares 是 SharesToWeight 的逆运算，用于把宿主既有 cpu.weight
// 反向呈现成用户熟悉的 shares（`licore stats` / 诊断输出用）。
func WeightToShares(weight int64) int64 {
	if weight <= 0 {
		return 0
	}
	return 2 + (weight-1)*262142/9999
}

// BlkioToIOWeight 把 --blkio-weight 的 [10,1000] 换算成 cgroups v2
// io.weight 的 [1,10000]，与 runc 公式一致：1 + (w-10)*9999/990。
func BlkioToIOWeight(weight int64) int64 {
	if weight == 0 {
		return 0
	}
	if weight < MinBlkioWeight {
		weight = MinBlkioWeight
	}
	if weight > MaxBlkioWeight {
		weight = MaxBlkioWeight
	}
	return 1 + (weight-MinBlkioWeight)*9999/(MaxBlkioWeight-MinBlkioWeight)
}

// ParseBytes 解析字节容量字符串，支持 k/m/g/t 与 ki/mi/gi/ti 后缀
// （大小写不敏感，可带小数）。纯数字按字节解释。空串返回 0。
//
// 采用 1024 进制（与 Docker 的 --memory=128M 行为一致）。
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	neg := strings.HasPrefix(s, "-")
	if neg {
		// 只允许 -1 这种哨兵值由调用方处理；其它负值交给数字解析报错。
		if s == "-1" {
			return -1, nil
		}
		return 0, fmt.Errorf("容量 %q 非法：不能为负: %w", s, ErrBadParam)
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	num, unit := s[:i], strings.TrimSpace(s[i:])
	if num == "" {
		return 0, fmt.Errorf("容量 %q 非法：缺少数字: %w", s, ErrBadParam)
	}
	val, err := parseFloat(num)
	if err != nil {
		return 0, fmt.Errorf("容量 %q 非法: %w", s, ErrBadParam)
	}
	mult, err := byteUnit(unit)
	if err != nil {
		return 0, fmt.Errorf("容量 %q: %w", s, err)
	}
	if val > math.MaxInt64/float64(mult) {
		return 0, fmt.Errorf("容量 %q 溢出: %w", s, ErrBadParam)
	}
	return int64(val * float64(mult)), nil
}

// parseFloat 是 strconv.ParseFloat 的薄包装，统一错误文案。
func parseFloat(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("解析数字 %q: %w", s, err)
	}
	return f, nil
}

// byteUnit 把单位后缀换算成乘数；无后缀为 1。
func byteUnit(unit string) (int64, error) {
	switch strings.ToLower(strings.TrimSuffix(unit, "b")) {
	case "":
		return 1, nil
	case "k", "ki":
		return 1 << 10, nil
	case "m", "mi":
		return 1 << 20, nil
	case "g", "gi":
		return 1 << 30, nil
	case "t", "ti":
		return 1 << 40, nil
	case "p", "pi":
		return 1 << 50, nil
	}
	return 0, fmt.Errorf("未知容量单位 %q（支持 k/m/g/t 与 ki/mi/gi/ti）: %w", unit, ErrBadParam)
}

// ParseBandwidth 解析带宽字符串，如 "10mbps"、"1.5M"、"500kbps"。
// 返回字节/秒。无后缀按字节/秒解释；bps 后缀（bit/s）按 8 折算成字节/秒。
func ParseBandwidth(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	low := strings.ToLower(s)
	bits := false
	switch {
	case strings.HasSuffix(low, "bit"), strings.HasSuffix(low, "bits"):
		bits = true
		low = strings.TrimSuffix(strings.TrimSuffix(low, "bits"), "bit")
	case strings.HasSuffix(low, "bps"):
		bits = true
		low = strings.TrimSuffix(low, "bps")
	case strings.HasSuffix(low, "ps"):
		low = strings.TrimSuffix(low, "ps")
	}
	v, err := ParseBytes(low)
	if err != nil {
		return 0, fmt.Errorf("带宽 %q: %w", s, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("带宽 %q 非法：必须是正数: %w", s, ErrBadParam)
	}
	if bits {
		v /= 8
	}
	if v <= 0 {
		return 0, fmt.Errorf("带宽 %q 折合后小于 1 B/s: %w", s, ErrBadParam)
	}
	return v, nil
}

// CPUSetValid 校验 cpuset 列表语法：逗号分隔的 "n" 或 "a-b"，都为非负整数。
func CPUSetValid(s string) bool {
	if s == "" {
		return true
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		lo, hi, hasRange := strings.Cut(part, "-")
		a, err := parseNonNegInt(lo)
		if err != nil {
			return false
		}
		if !hasRange {
			continue
		}
		b, err := parseNonNegInt(hi)
		if err != nil || b < a {
			return false
		}
	}
	return true
}

// parseNonNegInt 解析非负十进制整数。
func parseNonNegInt(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("空数字: %w", ErrBadParam)
	}
	var v int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%q 不是非负整数: %w", s, ErrBadParam)
		}
		v = v*10 + int64(c-'0')
		if v > math.MaxInt32 {
			return 0, fmt.Errorf("%q 过大: %w", s, ErrBadParam)
		}
	}
	return v, nil
}

// FormatBytes 把字节数格式化成人类可读形式（KiB/MiB/GiB，保留一位小数）。
func FormatBytes(n int64) string {
	switch {
	case n < 0:
		return "unlimited"
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n < 1<<40:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.1f TiB", float64(n)/(1<<40))
}

// FormatRate 把字节/秒格式化成人类可读形式。
func FormatRate(bps int64) string {
	if bps <= 0 {
		return "unlimited"
	}
	return FormatBytes(bps) + "/s"
}

// FormatDuration 把核·秒累计用量格式化成人类可读形式（stats 的 CPU 列）。
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Millisecond).String()
}
