// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/engine"
	"github.com/LiStudioorg/boxli/internal/resource"
	"github.com/LiStudioorg/boxli/internal/store"
)

// newRunCommand 实现 `boxli run`：镜像查找 → 层解包合并 rootfs → 写容器
// 状态 → 前台持有或后台 fork shim。编排逻辑在 internal/engine。
func newRunCommand(out io.Writer) *cobra.Command {
	var opts struct {
		detach       bool
		restart      string
		name         string
		hostname     string
		memoryMB     int
		memorySwapMB int
		memoryResMB  int
		cpus         float64
		pidsLimit    int
		cpuset       string
		blkioWeight  int
		storageMB    int
		networkBw    string
		gpu          int
		npu          int
		env          []string
		workdir      string
		user         string
		entrypoint   []string
		ports        []string
		volumes      []string
		dataDir      string
	}
	cmd := &cobra.Command{
		Use:   "run [flags] <image> [command...]",
		Short: "创建并启动容器",
		Long: "创建并启动容器：镜像查找 → 层解包合并 rootfs → 写容器状态 → 启动。\n\n" +
			"默认前台运行，stdio 直连容器，Ctrl+C 停止容器；-d 后台运行并打印容器 ID。\n\n" +
			"注意：run 自身的 flag 必须写在镜像引用之前，镜像之后的内容一律作为容器命令\n" +
			"原样传入（与 Docker 一致）。例如 `boxli run -e FOO=bar img sh -c 'echo $FOO'`。",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			restart := store.Restart(opts.restart)
			if !restart.Valid() {
				return fmt.Errorf("run: 非法 --restart 值 %q（可选：no|always|unless-stopped|on-failure）", opts.restart)
			}
			// 阶段 3 占位参数：记录进配置、暂不生效，明确告警不静默。
			for _, p := range opts.ports {
				slog.Warn("-p 端口映射尚未实现（阶段 3 网络落地），本次运行忽略", "mapping", p)
			}
			for _, v := range opts.volumes {
				slog.Warn("-v 宿主机卷挂载尚未实现（阶段 3 落地），本次运行忽略", "volume", v)
			}
			lims, err := runLimits(opts.memoryMB, opts.memorySwapMB, opts.memoryResMB, opts.cpus, opts.pidsLimit, opts.cpuset, opts.blkioWeight, opts.storageMB, opts.networkBw, opts.gpu, opts.npu)
			if err != nil {
				return err
			}

			st, err := store.Open(opts.dataDir)
			if err != nil {
				return err
			}
			spec := &engine.RunSpec{
				ImageRef:   args[0],
				Cmd:        args[1:],
				Env:        opts.env,
				Name:       opts.name,
				Hostname:   opts.hostname,
				Workdir:    opts.workdir,
				User:       opts.user,
				Entrypoint: opts.entrypoint,
				Restart:    restart,
				Detach:     opts.detach,
				Ports:      opts.ports,
				Volumes:    opts.volumes,
				MemoryMB:   opts.memoryMB,
				CPUs:       opts.cpus,
				PidsLimit:  opts.pidsLimit,
			}

			// 前台模式：Ctrl+C（SIGINT/SIGTERM）→ ctx 取消 → engine 转发容器。
			ctx := context.Background()
			if !opts.detach {
				stop := make(chan os.Signal, 1)
				signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
				defer signal.Stop(stop)
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				go func() {
					<-stop
					cancel()
				}()
			}

			res, err := engine.Run(ctx, st, spec)
			if res != nil && res.Container != nil {
				// 无论成败先公布容器 ID，便于 rm/logs 排查。
				if opts.detach {
					fmt.Fprintf(out, "%s\n", res.ShortID)
				}
			}
			if err != nil {
				return err
			}
			// 资源限制：为容器创建 cgroup 并写入；无权限/非 Linux 时降级告警。
			if !lims.Empty() {
				if _, serr := resource.Setup(res.Container.ID, lims); serr != nil {
					slog.Warn("应用资源限制失败（可能需要 root 或 cgroups v2）", "container", res.Container.ID, "err", serr)
				}
			}
			if opts.detach {
				fmt.Fprintf(out, "容器 %s（%s）已在后台运行，shim 持有生命周期\n",
					res.Container.Name, res.Container.ID)
				return nil
			}
			if res.ExitCode != 0 {
				return &exitCodeError{code: res.ExitCode}
			}
			return nil
		},
	}
	f := cmd.Flags()
	// 容器命令里的 -c/-e 等必须原样透传（`boxli run img sh -c 'exit 1'`），
	// 故第一个位置参数（镜像引用）之后不再解析 flag。
	//
	// 代价与 Docker 一致：所有 run 自己的 flag 必须写在镜像引用之前，
	// 写在之后的会被当作容器命令参数。help 里明确写出这一点。
	f.SetInterspersed(false)
	f.BoolVarP(&opts.detach, "detach", "d", false, "后台运行，打印容器 ID")
	f.StringVar(&opts.restart, "restart", "no", "重启策略：no|always|unless-stopped|on-failure")
	f.StringVar(&opts.name, "name", "", "容器名（默认自动生成）")
	f.StringVar(&opts.hostname, "hostname", "", "容器主机名（默认取容器名）")
	f.IntVar(&opts.memoryMB, "memory", 0, "内存上限（MiB，0=不限制）")
	f.IntVar(&opts.memorySwapMB, "memory-swap", 0, "内存+swap 总上限（MiB，-1=不限制 swap）")
	f.IntVar(&opts.memoryResMB, "memory-reservation", 0, "内存软限制（MiB，0=不限制）")
	f.Float64Var(&opts.cpus, "cpus", 0, "CPU 配额（核数，0=不限制）")
	f.StringVar(&opts.cpuset, "cpuset-cpus", "", "允许使用的 CPU 列表（如 0-3,7）")
	f.IntVar(&opts.pidsLimit, "pids-limit", 0, "进程数上限（0=不限制）")
	f.IntVar(&opts.blkioWeight, "blkio-weight", 0, "块设备相对权重 [10,1000]，0=不设置")
	f.IntVar(&opts.storageMB, "storage", 0, "可写层存储配额（MiB，0=不限制）")
	f.StringVar(&opts.networkBw, "network-bandwidth", "", "出向带宽上限（如 10mbps）")
	f.IntVar(&opts.gpu, "gpu", 0, "直通 GPU 设备数（0=不直通）")
	f.IntVar(&opts.npu, "npu", 0, "直通 NPU 设备数（0=不直通）")
	f.StringArrayVarP(&opts.env, "env", "e", nil, "环境变量 KEY=VALUE，可重复")
	f.StringVar(&opts.workdir, "workdir", "", "工作目录")
	f.StringVar(&opts.user, "user", "", "运行用户 uid:gid（阶段 3 生效）")
	f.StringSliceVar(&opts.entrypoint, "entrypoint", nil, "覆盖镜像 entrypoint")
	f.StringSliceVarP(&opts.ports, "publish", "p", nil, "端口映射 HOST:CONTAINER（阶段 3 生效）")
	f.StringSliceVarP(&opts.volumes, "volume", "v", nil, "卷挂载 HOST:CONTAINER（阶段 3 生效）")
	f.StringVar(&opts.dataDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	return cmd
}

// runLimits 把 run 的 CLI 资源参数翻译成 resource.Limits。
func runLimits(memoryMB, memorySwapMB, memoryResMB int, cpus float64, pidsLimit int,
	cpuset string, blkioWeight, storageMB int, networkBw string, gpu, npu int,
) (*resource.Limits, error) {
	l := &resource.Limits{}
	mb := func(v int) int64 { return int64(v) * 1024 * 1024 }
	if memoryMB > 0 {
		l.Memory = mb(memoryMB)
	}
	if memoryResMB > 0 {
		l.MemoryReservation = mb(memoryResMB)
	}
	l.MemorySwap = mb(memorySwapMB)
	if memorySwapMB == -1 {
		l.MemorySwap = -1
	}
	if cpus > 0 {
		l.CPUs = cpus
	}
	l.CPUSet = cpuset
	if pidsLimit > 0 {
		l.PidsLimit = int64(pidsLimit)
	}
	if blkioWeight > 0 {
		l.BlkioWeight = int64(blkioWeight)
	}
	if storageMB > 0 {
		l.Storage = mb(storageMB)
	}
	if networkBw != "" {
		v, err := resource.ParseBandwidth(networkBw)
		if err != nil {
			return nil, fmt.Errorf("run: %w", err)
		}
		l.NetworkBandwidth = v
	}
	if gpu > 0 {
		l.GPU = []resource.DeviceRequest{{Kind: "gpu", Count: gpu}}
	}
	if npu > 0 {
		l.NPU = []resource.DeviceRequest{{Kind: "npu", Count: npu}}
	}
	return l, nil
}
