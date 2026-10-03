// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/network"
)

// newNetworkCommand 实现 `licore network` 命令树：网络驱动的创建、列举、
// 检查、删除与容器接入/断开。
func newNetworkCommand(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "network",
		Aliases: []string{"net"},
		Short:   "管理自定义网络（bridge / host / none）",
		Long:    "管理 LiCore 的自研容器网络：bridge（veth+网桥+NAT）、host、none。内置预置网桥 licore0。",
	}
	cmd.PersistentFlags().StringVar(&networkDataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")
	cmd.AddCommand(newNetworkLs(out))
	cmd.AddCommand(newNetworkCreate(out))
	cmd.AddCommand(newNetworkInspect(out))
	cmd.AddCommand(newNetworkRm(out))
	cmd.AddCommand(newNetworkConnect(out))
	cmd.AddCommand(newNetworkDisconnect(out))
	cmd.AddCommand(newNetworkDNS(out))
	return cmd
}

// networkDataDir 由 network 父命令的 persistent --data-dir 写入，子命令读取。
var networkDataDir string

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newNetworkLs(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "列出全部网络",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := network.NewManager(networkDataDir)
			if err != nil {
				return err
			}
			nets, err := m.List()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tDRIVER\tSUBNET\tENDPOINTS")
			for _, n := range nets {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", n.Name, n.Driver, orDash(n.Subnet), len(n.Endpoints))
			}
			return tw.Flush()
		},
	}
}

func newNetworkCreate(out io.Writer) *cobra.Command {
	var driver, subnet, gateway string
	var internal bool
	c := &cobra.Command{
		Use:   "create NAME",
		Short: "创建网络（默认 driver=bridge）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := network.Driver(driver)
			if !d.Valid() {
				return fmt.Errorf("network: 非法 --driver %q（可选：bridge|host|none）", driver)
			}
			m, err := network.NewManager(networkDataDir)
			if err != nil {
				return err
			}
			_ = internal
			n, err := m.Create(args[0], d, subnet, gateway)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\n", n.Name)
			if d == network.DriverBridge {
				fmt.Fprintf(out, "网桥 %s 已就绪（子网 %s，网关 %s）\n", n.Name, n.Subnet, n.Gateway)
			}
			return nil
		},
	}
	c.Flags().StringVar(&driver, "driver", "bridge", "驱动：bridge|host|none")
	c.Flags().StringVar(&subnet, "subnet", "", "bridge 网段 CIDR（如 172.18.0.0/16）")
	c.Flags().StringVar(&gateway, "gateway", "", "bridge 网关地址")
	c.Flags().BoolVar(&internal, "internal", false, "不提供对外 NAT 出口")
	return c
}

func newNetworkInspect(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect NAME",
		Short: "显示网络的详细信息",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := network.NewManager(networkDataDir)
			if err != nil {
				return err
			}
			n, err := m.Inspect(args[0])
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(n, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(out, string(data))
			return nil
		},
	}
}

func newNetworkRm(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:     "rm NAME",
		Aliases: []string{"remove"},
		Short:   "删除网络",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := network.NewManager(networkDataDir)
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

func newNetworkConnect(out io.Writer) *cobra.Command {
	var ip string
	c := &cobra.Command{
		Use:   "connect NETWORK CONTAINER",
		Short: "把容器接入网络（bridge 分配 IP）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := network.NewManager(networkDataDir)
			if err != nil {
				return err
			}
			cname := args[1]
			ep, err := m.Connect(args[0], cname, cname, ip)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "容器 %s 已接入 %s，IP=%s\n", cname, args[0], orDash(ep.IP))
			return nil
		},
	}
	c.Flags().StringVar(&ip, "ip", "", "指定容器 IP（可选，默认自动分配）")
	return c
}

func newNetworkDisconnect(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect NETWORK CONTAINER",
		Short: "断开容器与网络的连接",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := network.NewManager(networkDataDir)
			if err != nil {
				return err
			}
			if err := m.Disconnect(args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(out, "容器 %s 已从 %s 断开\n", args[1], args[0])
			return nil
		},
	}
}

func newNetworkDNS(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "dns NAME",
		Short: "解析容器名（内置 DNS）到 IP",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := network.NewManager(networkDataDir)
			if err != nil {
				return err
			}
			ip, err := network.NewDNSResolver(m).Resolve(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(out, ip)
			return nil
		},
	}
}
