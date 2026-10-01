// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package network

// 非 Linux 平台：网络实化统一返回 ErrUnsupported，定义管理仍可用（纯数据）。

func bridgeHostIface(n *Network) string { return bridgeHostInterface(n.Name) }

func hostDefaultIface() (string, error) { return "", ErrUnsupported }

func driverBootstrap(n *Network) error {
	if n.Driver != DriverBridge {
		return nil
	}
	return ErrUnsupported
}

func driverTeardown(n *Network) error {
	if n.Driver != DriverBridge {
		return nil
	}
	return ErrUnsupported
}

func bridgeAttachEndpoints(n *Network) error { return ErrUnsupported }

func bridgeDetachEndpoint(veth string) error { return ErrUnsupported }

func (n *Network) applyPortRules() error        { return ErrUnsupported }
func (n *Network) removePortRules(string) error { return ErrUnsupported }
