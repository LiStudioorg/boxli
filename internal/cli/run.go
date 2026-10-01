// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/engine"
	"github.com/LiStudioorg/boxli/internal/storage"
	"github.com/LiStudioorg/boxli/internal/store"
)

// newRunCommand 实现 `boxli run`：镜像查找 → 层解包合并 rootfs → 写容器
// 状态 → 前台持有或后台 fork shim。编排逻辑在 internal/engine。
func newRunCommand(out io.Writer) *cobra.Command {
	var opts struct {
		detach     bool
		restart    string
		name       string
		hostname   string
		memoryMB   int
		cpus       float64
		pidsLimit  int
		env        []string
		workdir    string
		user       string
		entrypoint []string
		ports      []string
		volumes    []string
		dataDir    string
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
			mounts, err := parseMounts(opts.volumes)
			if err != nil {
				return err
			}
			if opts.memoryMB > 0 || opts.cpus > 0 || opts.pidsLimit > 0 {
				slog.Warn("资源限制参数尚未生效（阶段 3 落地），本次运行忽略",
					"memoryMB", opts.memoryMB, "cpus", opts.cpus, "pidsLimit", opts.pidsLimit)
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
			// 卷接入：为匿名/命名卷在卷管理器落盘并记入容器挂载（数据层）。
			// 真实挂载点注入容器命名空间需运行时协作（见交接摘要）。
			if werr := wireVolumes(opts.dataDir, res.Container, mounts); werr != nil {
				slog.Warn("卷接入失败", "container", res.Container.ID, "err", werr)
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
	f.IntVar(&opts.memoryMB, "memory", 0, "内存上限（MiB，0=不限制；阶段 3 生效）")
	f.Float64Var(&opts.cpus, "cpus", 0, "CPU 配额（核数，0=不限制；阶段 3 生效）")
	f.IntVar(&opts.pidsLimit, "pids-limit", 0, "进程数上限（0=不限制；阶段 3 生效）")
	f.StringArrayVarP(&opts.env, "env", "e", nil, "环境变量 KEY=VALUE，可重复")
	f.StringVar(&opts.workdir, "workdir", "", "工作目录")
	f.StringVar(&opts.user, "user", "", "运行用户 uid:gid（阶段 3 生效）")
	f.StringSliceVar(&opts.entrypoint, "entrypoint", nil, "覆盖镜像 entrypoint")
	f.StringSliceVarP(&opts.ports, "publish", "p", nil, "端口映射 HOST:CONTAINER（阶段 3 生效）")
	f.StringSliceVarP(&opts.volumes, "volume", "v", nil, "卷挂载 SRC:TARGET[:ro]；SRC 可为宿主路径或命名卷，省略=匿名卷")
	f.StringVar(&opts.dataDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	return cmd
}

// VolumeMount 是一次 -v 的解析结果。
type VolumeMount struct {
	// Source 是宿主源（绝对路径）或命名卷名；空表示匿名卷。
	Source string
	// Target 是容器内挂载点。
	Target string
	// ReadOnly 是否只读。
	ReadOnly bool
	// Anonymous 是否匿名卷（Source 为空）。
	Anonymous bool
}

// parseMounts 解析 -v 参数列表。
// 支持 "TARGET"（匿名卷）、"SRC:TARGET"（bind/命名卷）、"SRC:TARGET:ro"。
func parseMounts(vols []string) ([]*VolumeMount, error) {
	var out []*VolumeMount
	for _, raw := range vols {
		parts := splitVol(raw)
		m := &VolumeMount{}
		switch len(parts) {
		case 1: // 匿名卷
			m.Target = parts[0]
			m.Anonymous = true
		case 2: // SRC:TARGET
			m.Source = parts[0]
			m.Target = parts[1]
		case 3: // SRC:TARGET:ro
			m.Source = parts[0]
			m.Target = parts[1]
			if parts[2] != "ro" {
				return nil, fmt.Errorf("run: 非法 -v 选项 %q（仅支持 ro）", parts[2])
			}
			m.ReadOnly = true
		default:
			return nil, fmt.Errorf("run: 非法 -v %q", raw)
		}
		if m.Target == "" {
			return nil, fmt.Errorf("run: -v %q 缺少容器路径", raw)
		}
		out = append(out, m)
	}
	return out, nil
}

func splitVol(s string) []string {
	return splitColonV(s)
}

func splitColonV(s string) []string {
	var out []string
	var cur []byte
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			out = append(out, string(cur))
			cur = cur[:0]
		} else {
			cur = append(cur, s[i])
		}
	}
	out = append(out, string(cur))
	return out
}

// wireVolumes 处理容器的卷：命名/匿名卷在卷管理器落盘并解析出源路径，
// bind 挂载直接记录源路径。真实挂载进容器命名空间由运行时协作。
func wireVolumes(dataDir string, cfg *store.ContainerConfig, mounts []*VolumeMount) error {
	if len(mounts) == 0 {
		return nil
	}
	vm, err := storage.NewVolumeManager(dataDir)
	if err != nil {
		return err
	}
	for _, m := range mounts {
		// bind：宿主绝对路径即源。
		if !m.Anonymous && len(m.Source) > 1 && m.Source[0] == '/' {
			continue
		}
		// 命名卷：引用已存在卷。
		if !m.Anonymous && m.Source != "" {
			if _, err := vm.Inspect(m.Source); err != nil {
				return fmt.Errorf("run: 卷 %q 不存在，请先 boxli volume create: %w", m.Source, err)
			}
			continue
		}
		// 匿名卷：自动生成名字并在卷管理器创建（已存在则复用）。
		name := "anon_" + cfg.ID
		if _, err := vm.Create(name, storage.DriverLocal, 0); err != nil && !errors.Is(err, storage.ErrVolumeExists) {
			return err
		}
		m.Source = name
		slog.Info("已为容器创建匿名卷", "container", cfg.ID, "vol", name)
	}
	return nil
}
