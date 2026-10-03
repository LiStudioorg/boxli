// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/resource"
	"github.com/LiStudioorg/licore/internal/store"
)

// newResourceCommand 实现 `boxli resource`：资源能力的诊断与查询。
func newResourceCommand(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resource",
		Short: "资源能力查询与诊断",
		Long:  "查看当前平台的资源限制能力（cgroups v2、配额、加速器直通）。",
	}
	cmd.AddCommand(newResourceInfo(out))
	cmd.AddCommand(newUpdateCommand(out))
	return cmd
}

func newResourceInfo(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "显示资源能力",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(out, "资源能力诊断:")
			fmt.Fprintf(out, "  cgroups v2 可用:   %v (%s)\n", resource.Available(), resource.CgroupV2Mount)
			fmt.Fprintf(out, "  boxli cgroup 组:   %s\n", resource.BoxliGroup)
			fmt.Fprintln(out, "  已实现的限制：")
			fmt.Fprintln(out, "    CPU 限制:          --cpus/--cpu-shares/--cpuset-cpus")
			fmt.Fprintln(out, "    内存限制:          --memory/--memory-swap/--memory-reservation")
			fmt.Fprintln(out, "    进程限制:          --pids-limit")
			fmt.Fprintln(out, "    IO 限制:           --blkio-weight/--device-read-bps 等")
			fmt.Fprintln(out, "  未实现（继续传入会明确报错）：")
			fmt.Fprintln(out, "    存储配额:          --storage")
			fmt.Fprintln(out, "    加速器直通:        --gpu / --npu")
			fmt.Fprintln(out, "    网络带宽:          --network-bandwidth")
			return nil
		},
	}
}

// newUpdateCommand 实现 `boxli update`：动态调整已运行容器的资源限制。
func newUpdateCommand(out io.Writer) *cobra.Command {
	var dataDir string
	var memoryMB, memorySwapMB, pidsLimit, blkioWeight int
	var cpus float64
	var cpuset string
	cmd := &cobra.Command{
		Use:   "update CONTAINER",
		Short: "动态调整容器的资源限制",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(dataDir)
			if err != nil {
				return err
			}
			c, err := st.FindContainer(args[0])
			if err != nil {
				return err
			}
			lims, err := runLimits(memoryMB, memorySwapMB, 0, cpus, pidsLimit,
				cpuset, blkioWeight, 0, "", 0, 0)
			if err != nil {
				return err
			}
			if lims.Empty() {
				return fmt.Errorf("update: 未提供任何可调整的限制参数")
			}
			if err := resource.Update(c.ID, lims); err != nil {
				return fmt.Errorf("update 容器 %s: %w", c.Name, err)
			}
			fmt.Fprintf(out, "已更新容器 %s 的资源限制\n", c.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	cmd.Flags().IntVar(&memoryMB, "memory", 0, "内存上限（MiB，0=不变）")
	cmd.Flags().IntVar(&memorySwapMB, "memory-swap", 0, "内存+swap 总上限（MiB，-1=不限 swap）")
	cmd.Flags().Float64Var(&cpus, "cpus", 0, "CPU 配额（核数，0=不变）")
	cmd.Flags().StringVar(&cpuset, "cpuset-cpus", "", "允许 CPU 列表")
	cmd.Flags().IntVar(&pidsLimit, "pids-limit", 0, "进程数上限（0=不变）")
	cmd.Flags().IntVar(&blkioWeight, "blkio-weight", 0, "块设备权重 [10,1000]")
	return cmd
}
