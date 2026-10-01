// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package shim 实现每容器生命周期持有者（类比 Podman 的 conmon）。
//
// 模型（AGENTS.md《开机自启动机制》）：引擎本体不常驻；`boxli run -d` /
// `boxli boot` 通过 Reexec 把当前二进制重执行为一个脱离终端的 shim 进程，
// shim 进程 = 容器之父：启动容器 → 记录 runtime.json → 按 restart 策略
// 决定重启或退出。容器 init 由 internal/runtime 经 namespace + pivot_root
// 创建；shim 崩溃时容器 init 随 namespace 残留但不会僵死循环。
//
// 停止语义（无 daemon，stop 经由状态文件 + 信号协作）：
//   - `boxli stop`：写 stopped-by-user 标记 → SIGTERM 容器 init（runtime.json
//     里有 PID）→ init 退出 → shim 观察到标记后按策略决定去留
//     （unless-stopped 退出；always 清标记继续重启，与 AGENTS.md 一致）。
//   - 直接 SIGTERM shim（如 systemd ExecStop）：shim 转发 SIGTERM 给 init
//     并退出，不再重启。
package shim
