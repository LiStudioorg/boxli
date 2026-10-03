// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/storage"
)

// volumeDataDir 由 volume 父命令的 persistent --data-dir 写入，子命令读取。
var volumeDataDir string

// newVolumeCommand 实现 `licore volume` 命令树：命名卷的创建、列举、
// 检查、删除、清理与快照/克隆。
func newVolumeCommand(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volume",
		Aliases: []string{"vol"},
		Short:   "管理数据卷（local / tmpfs）",
		Long:    "管理 LiCore 的命名数据卷：创建、列举、检查、删除、清理，以及快照与克隆。",
	}
	cmd.PersistentFlags().StringVar(&volumeDataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")
	cmd.AddCommand(newVolumeCreate(out))
	cmd.AddCommand(newVolumeLs(out))
	cmd.AddCommand(newVolumeInspect(out))
	cmd.AddCommand(newVolumeRm(out))
	cmd.AddCommand(newVolumePrune(out))
	cmd.AddCommand(newVolumeSnapshot(out))
	cmd.AddCommand(newVolumeClone(out))
	return cmd
}

func volumeMgr() (*storage.VolumeManager, error) { return storage.NewVolumeManager(volumeDataDir) }

func volumeSizeText(v *storage.Volume) string {
	if v.Size <= 0 {
		return "-"
	}
	return strconv.FormatInt(v.Size, 10)
}

func newVolumeCreate(out io.Writer) *cobra.Command {
	var driver string
	var sizeMB int
	c := &cobra.Command{
		Use:   "create NAME",
		Short: "创建命名卷（默认 driver=local）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := volumeMgr()
			if err != nil {
				return err
			}
			v, err := m.Create(args[0], driver, int64(sizeMB)*1024*1024)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\n", v.Name)
			return nil
		},
	}
	c.Flags().StringVar(&driver, "driver", "local", "驱动：local|tmpfs")
	c.Flags().IntVar(&sizeMB, "size", 0, "配额（MiB，0=不限）")
	return c
}

func newVolumeLs(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "列出全部卷",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := volumeMgr()
			if err != nil {
				return err
			}
			vols, err := m.List()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tDRIVER\tSIZE(B)\tMOUNTPOINT")
			for _, v := range vols {
				mp, _ := m.Mountpoint(v.Name)
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", v.Name, v.Driver, volumeSizeText(v), mp)
			}
			return tw.Flush()
		},
	}
}

func newVolumeInspect(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect NAME",
		Short: "显示卷的详细信息",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := volumeMgr()
			if err != nil {
				return err
			}
			v, err := m.Inspect(args[0])
			if err != nil {
				return err
			}
			mp, _ := m.Mountpoint(v.Name)
			fmt.Fprintf(out, "Name:      %s\n", v.Name)
			fmt.Fprintf(out, "Driver:    %s\n", v.Driver)
			fmt.Fprintf(out, "Size(B):   %s\n", volumeSizeText(v))
			fmt.Fprintf(out, "Mount:     %s\n", mp)
			fmt.Fprintf(out, "Created:   %s\n", v.CreatedAt)
			return nil
		},
	}
}

func newVolumeRm(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:     "rm NAME",
		Aliases: []string{"remove"},
		Short:   "删除卷（数据一并删除）",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := volumeMgr()
			if err != nil {
				return err
			}
			if err := m.Remove(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\n", args[0])
			return nil
		},
	}
}

func newVolumePrune(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "删除所有未使用的卷",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := volumeMgr()
			if err != nil {
				return err
			}
			n, err := m.Prune()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "删除了 %d 个卷\n", n)
			return nil
		},
	}
}

func newVolumeSnapshot(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot NAME",
		Short: "为卷创建只读快照",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := volumeMgr()
			if err != nil {
				return err
			}
			path, err := m.Snapshot(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(out, path)
			return nil
		},
	}
}

func newVolumeClone(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "clone FROM TO",
		Short: "把卷克隆到新卷",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := volumeMgr()
			if err != nil {
				return err
			}
			newv, err := m.Clone(args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\n", newv.Name)
			return nil
		},
	}
}
