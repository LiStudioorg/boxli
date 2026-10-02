#!/usr/bin/env bash
# Copyright (C) 2026 LiStudioorg
# SPDX-License-Identifier: AGPL-3.0-only
#
# boxli v0.5.x root 真机验证脚本。
#
# 用法：以 root 运行：  sudo bash docs/verify-root.sh   （或 root 下直接 bash）
# 完整输出（stdout+stderr）追加到 /tmp/boxli-verify.log，结束后可 cat 查看回贴。
#
# 设计：
#   - set -e：任一步失败立即停并打印诊断
#   - 每步先 echo 期望输出，再执行并回显实际输出
#   - 幂等：开头自动清理残留（旧容器、cgroup、veth、NAT、本地 hub、demo 文件）
#   - 结束后打印 PASS/FAIL 计数

set -euo pipefail

LOG=/tmp/boxli-verify.log
: > "$LOG"   # 每次运行从空日志开始（可重复跑）
exec > >(tee -a "$LOG") 2>&1

PASS=0; FAIL=0
pass() { PASS=$((PASS+1)); echo "[PASS] $*"; }
fail() { FAIL=$((FAIL+1)); echo "[FAIL] $*"; }
step() { echo; echo "===== $* ====="; }
ok()   { echo "  -> ok: $*"; }
skip() { echo "  -> skipped: $*"; }

# 环境信息
step "环境信息"
echo "  内核: $(uname -r)  ($(uname -m))"
echo "  架构: $(uname -m)"
echo "  用户: $(id -u 2>/dev/null) ($(id -un 2>/dev/null))"
echo "  cgroup: $(cat /proc/self/cgroup 2>/dev/null | head -1)"
if [ -e /sys/fs/cgroup/cgroup.controllers ]; then echo "  cgroup v2: yes"; else echo "  cgroup v2: NO"; fi
if command -v nft >/dev/null 2>&1; then echo "  nft: $(nft --version 2>&1 | head -1)"; else echo "  nft: NOT FOUND"; fi
if command -v ip >/dev/null 2>&1; then echo "  iproute2: $(ip -V 2>&1 | head -1)"; else echo "  iproute2: NOT FOUND"; fi
if command -v curl >/dev/null 2>&1; then echo "  curl: $(curl --version 2>&1 | head -1)"; else echo "  curl: NOT FOUND"; fi

# boxli 路径
BOXLI_BIN="${BOXLI_BIN:-/usr/local/bin/boxli}"
echo "  boxli: $BOXLI_BIN ($(readlink -f "$BOXLI_BIN" 2>/dev/null || echo missing))"
"$BOXLI_BIN" --version 2>/dev/null || echo "  (boxli --version 不可用)"

# boxli 数据目录（隔离，避免污染）
BOXLI_HOME="${BOXLI_HOME:-/root/.boxli}"
export BOXLI_HOME
WORK=/tmp/boxli-verify-work
mkdir -p "$WORK"
echo "  数据目录: $BOXLI_HOME"

############ 前置检查 ############
step "前置检查"
if [ "$(id -u)" != "0" ]; then fail "需要 root（当前 uid=$(id -u)）"; else pass "root 权限"; fi
grep -q '^1 ' /proc/self/cgroup 2>/dev/null || true
if [ -e /sys/fs/cgroup/cgroup.controllers ]; then pass "cgroup v2"; else fail "cgroup v2 未挂载"; fi
command -v nft >/dev/null 2>&1 && pass "nft 存在" || fail "缺 nft（apt install nftables）"
command -v ip  >/dev/null 2>&1 && pass "iproute2 存在" || fail "缺 iproute2"
command -v curl >/dev/null 2>&1 && pass "curl 存在" || fail "缺 curl"
command -v go  >/dev/null 2>&1 && pass "go 存在（构建 demo 用）" || fail "缺 go（构建 demo 二进制需要）"

############ 清理残留（幂等起点） ############
step "清理残留（旧容器 / cgroup / veth / NAT / hub / demo 文件）"
# 停止并删除 demo 容器（-f 强制）
"$BOXLI_BIN" rm -f demo demo2 demo3 2>/dev/null || true
"$BOXLI_BIN" stop demo demo2 demo3 2>/dev/null || true
# 输出容器名里的 demo 全停全删
containers=$("$BOXLI_BIN" ps -a 2>/dev/null | awk 'NR>1{print $1}') || true
for c in $containers; do
  "$BOXLI_BIN" rm -f "$c" 2>/dev/null || true
done
# 删除 boxli cgroup 组
if [ -d /sys/fs/cgroup/boxli ]; then
  rmdir /sys/fs/cgroup/boxli/* 2>/dev/null || true
fi
rmdir /sys/fs/cgroup/boxli 2>/dev/null || true
# 删除 boxli0 网桥与残留 veth（grep 无匹配时退出 1，pipefail 下须兜底）
ip link del boxli0 2>/dev/null || true
leaked_veth=$(ip link show 2>/dev/null | grep -oE 'veth[a-f0-9]{7}|vpe[a-f0-9]{7}' | sort -u) || true
for if in $leaked_veth; do
  ip link del "$if" 2>/dev/null || true
done
# 清空 nft boxli 表
nft flush table ip boxli 2>/dev/null || true
nft delete table ip boxli 2>/dev/null || true
# 清掉本地 hub serve 残留
pkill -f 'boxli hub serve' 2>/dev/null || true
rm -rf "$WORK/demo-src" "$WORK/hub" 2>/dev/null || true
# 删旧 demo 镜像（若 build 会覆盖则不用删，避免破坏已有数据）
echo "  清理完成"

############ 构建 demo:v1 ############
step "A. 构建 demo:v1 "
# 自包含一个静态 Go HTTP server + exec 检查子命令
mkdir -p "$WORK/demo-src"
cat > "$WORK/demo-src/demo.go" <<'GOEOF'
package main

import (
    "fmt"
    "net/http"
    "os"
    "os/exec"
    "strings"
)

func main() {
    // `demo check`：供 boxli exec 验证命名空间/挂载/环境。
    if len(os.Args) > 1 && os.Args[1] == "check" {
        h, _ := os.Hostname()
        pid := os.Getpid()
        out, _ := os.ReadFile("/proc/self/cgroup")
        fmt.Printf("hostname=%s pid=%d cgroup=%s\n", h, pid, strings.TrimSpace(string(out)))
        return
    }
    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        _, _ = fmt.Fprint(w, "hello from boxli\n")
    })
    _ = exec.Command // 保留引用，确保静态链接无依赖
    _ = http.ListenAndServe(":80", nil)
}
GOEOF
( cd "$WORK/demo-src" && CGO_ENABLED=0 go build -o server ./demo.go ) || fail "编译 demo 静态二进制失败"
cat > "$WORK/demo-src/Boxfile" <<'BOXEOF'
FROM scratch
COPY server /server
ENTRYPOINT ["/server"]
BOXEOF
( cd "$WORK/demo-src" && "$BOXLI_BIN" build -t demo:v1 --file "$WORK/demo-src/Boxfile" . )
"$BOXLI_BIN" images | grep -q 'demo' && pass "demo:v1 已在镜像列表" || fail "demo:v1 未导入"

# 容器名 -> ID（boxli 无 inspect，靠 ps -a 解析）
cid_of() { "$BOXLI_BIN" ps -a 2>/dev/null | awk -v n="$1" '$2==n{print $1}' | head -1; }
# 容器名是否 Up（ps 状态列是第 4 列）
is_up() { "$BOXLI_BIN" ps -a 2>/dev/null | awk -v n="$1" '$2==n{ if ($4 ~ /^Up/) print "yes" }' | grep -q 'yes'; }

############ B. 运行 + 网络 ############
step "B. 运行 demo（网络 boxli0 + 端口 18080:80）"
"$BOXLI_BIN" run -d --name demo --network boxli0 -p 18080:80 demo:v1
# 等待启动（最多 20s，避免装配卡顿）
for i in $(seq 1 20); do
  is_up demo && break
  sleep 1
done
"$BOXLI_BIN" ps -a
if is_up demo; then
  pass "ps 显示 demo Up"
else
  fail "ps 未显示 demo Up（看上面实际输出）"
  echo "  --- container.log ---"; cat "$BOXLI_HOME/containers/$(cid_of demo)/container.log" 2>/dev/null || true
fi

step "验证容器进程真在运行"
if is_up demo; then
  initpid=$(cid_of demo)
  initpid=$("$BOXLI_BIN" exec demo /server check 2>/dev/null | grep -oE 'pid=[0-9]+' | grep -oE '[0-9]+' | head -1 || true)
  echo "  容器内 /server check -> pid=$initpid"
  [ -n "${initpid:-}" ] && pass "容器进程存活 (pid=$initpid)" || fail "取不到容器进程 pid（exec 未通）"
else
  fail "容器未在运行，跳过进程验证"
fi

step "验证 veth 对（master=boxli0）"
if ip link show 2>/dev/null | grep -q 'master boxli0'; then
  ip link show 2>/dev/null | grep 'master boxli0' | head
  pass "存在挂到 boxli0 的 veth"
else
  fail "未见 master boxli0 的 veth"
fi

step "验证端口 18080 返回 hello"
sleep 1
out=$(curl -s --max-time 5 http://127.0.0.1:18080/ 2>/dev/null || true)
echo "  curl -> $out"
if echo "$out" | grep -q 'hello'; then pass "curl 返回 hello"; else fail "curl 未返回 hello"; fi

############ D. 资源限制 ############
step "D. 资源限制（--memory 256 --cpus 1）"
"$BOXLI_BIN" run -d --name demo3 --network boxli0 --memory 256 --cpus 1 demo:v1
for _ in 1 2 3 4 5; do cid3=$(cid_of demo3); [ -n "$cid3" ] && break; sleep 1; done
echo "  demo3 id=$cid3"
cgrp="/sys/fs/cgroup/boxli/$cid3"
mem=$(cat "$cgrp/memory.max" 2>/dev/null || echo missing)
cpu=$(cat "$cgrp/cpu.max" 2>/dev/null || echo missing)
procs=$(cat "$cgrp/cgroup.procs" 2>/dev/null || echo missing)
echo "  memory.max=$mem (期望 268435456)" ;  echo "  cpu.max=$cpu (期望 100000 100000)"; echo "  cgroup.procs=$procs"
[ "$mem" = "268435456" ] && pass "memory.max=268435456" || fail "memory.max= $mem"
[ "$cpu" = "100000 100000" ] && pass "cpu.max=100000 100000" || fail "cpu.max= $cpu"
[ -n "$procs" ] && pass "cgroup.procs 含 PID" || fail "cgroup.procs 为空"

############ E. exec ############
step "E. exec 进入容器"
eout=$("$BOXLI_BIN" exec demo3 /server check 2>&1 || true)
echo "  exec -> $eout"
if echo "$eout" | grep -q 'hostname=.*pid='; then pass "exec 进入容器成功"; else fail "exec 失败"; fi

############ F. stop/rm 秒退 ############
step "F. stop / rm 秒退（>5s 判 FAIL）"
t0=$(date +%s); "$BOXLI_BIN" stop demo3; t1=$(date +%s); dt=$((t1-t0))
echo "  stop 耗时 ${dt}s"
[ "$dt" -le 5 ] && pass "stop 秒退 (${dt}s)" || fail "stop 太慢 (${dt}s)"
t0=$(date +%s); "$BOXLI_BIN" rm -f demo demo3; t1=$(date +%s); dt=$((t1-t0))
echo "  rm 耗时 ${dt}s"
[ "$dt" -le 5 ] && pass "rm 秒退 (${dt}s)" || fail "rm 太慢 (${dt}s)"

############ G. Hub ###########
step "G. hub serve -> login -> push -> search -> pull"
HS=$WORK/hub; mkdir -p "$HS"
"$BOXLI_BIN" hub serve --port 7399 --data-dir "$HS" --username admin --password admin &
HSRV=$!
sleep 1
export BOXLI_HUB=http://127.0.0.1:7399
BOXLI_HOME="$HS" "$BOXLI_BIN" login http://127.0.0.1:7399 --username admin --password admin --data-dir "$HS" >/dev/null 2>&1 \
  && pass "hub login" || fail "hub login"
BOXLI_HOME="$HS" "$BOXLI_BIN" push demo:v1 "$BOXLI_HOME/images/demo/v1/source.boxli" --hub http://127.0.0.1:7399 --data-dir "$HS" >/dev/null 2>&1 \
  && pass "hub push" || fail "hub push"
if BOXLI_HOME="$HS" "$BOXLI_BIN" search demo --hub http://127.0.0.1:7399 --data-dir "$HS" 2>/dev/null | grep -q demo; then
  pass "hub search"; else fail "hub search"; fi
BOXLI_HOME="$HS" "$BOXLI_BIN" pull demo:v1 --hub http://127.0.0.1:7399 --data-dir "$HS" >/dev/null 2>&1 \
  && pass "hub pull" || fail "hub pull"
kill "$HSRV" 2>/dev/null || true

############ 清理 ############
step "清理 & 无残留检查"
"$BOXLI_BIN" stop demo 2>/dev/null || true
"$BOXLI_BIN" rm -f demo demo2 demo3 2>/dev/null || true
sleep 1
# 无残留进程
left_proc=$("$BOXLI_BIN" ps 2>/dev/null | grep -v '^CONTAINER' | awk 'NF>0{print}')
[ -z "$left_proc" ] && pass "无残留容器" || fail "仍有容器: $left_proc"
# 无残留 veth
if ip link show 2>/dev/null | grep -q 'master boxli0'; then fail "残留 veth"; else pass "无残留 veth"; fi
# 无残留 cgroup
if ls /sys/fs/cgroup/boxli/ 2>/dev/null | grep -q .; then fail "残留 cgroup"; else pass "无残留 cgroup"; fi
# 无残留 mount
if mount 2>/dev/null | grep -q "boxli"; then fail "残留 boxli mount"; else pass "无残留 mount"; fi

############ 汇总 ############
step "汇总"
echo "  PASS=$PASS  FAIL=$FAIL"
echo
echo "完整日志：cat $LOG"
[ "$FAIL" -eq 0 ] && echo "== ALL PASS ==" || echo "== THERE ARE FAILURES =="
exit 0
