# Boxli 真机测试报告 v0.6.0

环境：root（免密 sudo，生产 ECS，含 Docker 业务）；内核 5.15.0-179；内核带 rootless-docker + ufw（FORWARD DROP）。
方法：隔离数据目录 `/tmp/boxli-test-home`，只操作 boxli 自有资源；测试前后对比宿主快照，确认未污染 Docker/netfilter。

## 汇总

| 项 | 结果 | 说明 |
| --- | --- | --- |
| A 构建/images | ✅ PASS | |
| B 容器生命周期 | ✅ PASS | |
| C 网络 | ✅ PASS | 直连 + 端口映射 SKIP（宿主限制）|
| D 卷 | ✅ PASS | 含 :ro 只读修复 |
| E 资源限制 | ✅ PASS | 含 cgroup subtree_control 修复 |
| F exec | ✅ PASS | 含 cgo 进命名空间修复 |
| G Hub | ✅ PASS | |
| H 清理/无残留 | ✅ PASS | |
| I 异常场景 | ✅ PASS* | 并发同名修复 |
| J 开机自启 | ✅ PASS | unit 生成/内容（未实际 enable）|

PASS=10（其中 3 项测试过程中修复了 boxli bug），SKIP=1（端口映射，宿主限制），FAIL=0。

## A. 构建

```
boxli build -f Boxfile -t demo:v1 <ctx>   → 已构建并导入 demo:v1（1 层 4846 KiB）
boxli images                              → REPOSITORY demo  TAG v1  ARCH amd64  LAYERS 1  SIZE 5.0 MB
```

## B. 容器生命周期

```
boxli run -d --name demo --network boxli0 -p 18080:80 demo:v1
boxli ps -a        → demo  Up 3s（不卡 Starting）
ps aux | grep server → /server  PID 830680（真实进程）
boxli stop demo    → 耗时 129ms（<5s）
boxli rm -f demo   → 耗时 88ms（<2s）
```

## C. 网络

```
boxli0: inet 172.20.0.1/16 state UP（自动避让 docker 的 172.18.0.0/16）
容器 IP: demo=172.20.0.2, demo2=172.20.0.3（同 boxli0 桥，L2 互连）
host 直连容器 IP: curl 172.20.0.2/ → hello from boxli ✅
veth: vethX@ifY master boxli0 state UP
DNAT: tcp dport 18080 dnat to 172.20.0.2:80（boxli 规则已装配）
端口映射 18080 → SKIP（宿主 rootless-docker+FORWARD DROP 禁止 host→容器端口
  转发；Docker 自身 new-api 3001->3000 同样不通，确证宿主限制非 boxli bug）
```

## D. 卷

```
boxli run -d -v /tmp/boxli-vol:/data -v readonly:/static:ro demo:v1
exec /check → vol=host-data-12345（host 目录可见）
容器写 /data/written.txt → host 读到（数据持久写回）✅
容器写 /static（:ro）→ DENIED（:ro 生效）✅（本次修复 bug：需 bind 后 remount ro）
```

## E. 资源限制

```
boxli run -d --name demo3 --memory 256 --cpus 1 --pids-limit 100 demo:v1
/sys/fs/cgroup/boxli/<id>/memory.max = 268435456
/sys/fs/cgroup/boxli/<id>/cpu.max    = 100000 100000
/sys/fs/cgroup/boxli/<id>/pids.max   = 100
/sys/fs/cgroup/boxli/<id>/cgroup.procs = <PID>
cgroup subtree_control = cpu memory pids（boxli 自 enable，本次修复）
```

## F. exec（cgo 进命名空间）

```
boxli exec demo /check → hostname=demo pid=<容器内> resolv=nameserver 172.20.0.1
boxli exec -w /tmp demo /check → cwd=/tmp
boxli exec -e FOO=bar demo /check → FOO=bar
boxli exec -u 65534 demo /check → uid=65534
```

## G. Hub

```
hub serve --port 3727 &
login   → 已登录 admin，令牌已缓存
push    → 已推送 demo:v1
search  → demo  v1  digest:8fd2…
pull    → 成功
无 token 请求 blob → HTTP 401（JWT 鉴权生效）
```

## H. 清理 / 无残留

rm 后：无进程、无 boxli nft 表、无容器 cgroup、无 veth、无 boxli mount、容器目录空。

## I. 异常场景

- 并发 `run --name demo`（两个同时）→ 恰 1 个成功、1 个报"同名容器已存在"（本次修复：原子名字锁）
- 容器内 SIGKILL PID 1 → `Exited (137)`，shim 正确感知

## J. 开机自启

```
boxli boot enable  → 生成 /etc/systemd/system/boxli.service（oneshot + RemainAfterExit，ExecStart=boot/ExecStop=shutdown）
systemd 内容正确（Use 隔离 --data-dir）
boxli boot disable → 移除 unit
```
（按要求未实际 `systemctl enable` 生效，仅验证 unit 生成/内容。）

## SKIP 项及原因

| 项 | 原因 |
| --- | --- |
| 端口映射 18080（host→容器）| 宿主 rootless-docker + FORWARD DROP + ufw 禁止转发；Docker 自身映射也不通（对照证明）。需在标准 root 主机验证。|

## 修复的 bug（测试中发现）

1. cgroup 资源限制落空（subtree_control 未 enable）→ `fix(resource)` `8f04018`
2. exec setns(mnt) EINVAL（纯 Go 限制）→ cgo fork 子进程 `fix(exec)` `39ccd18`
3. :ro 卷只读没生效 → bind 后 remount `fix(volume)` `d241797`
4. 并发 run 同名竞态 + rm 泄漏名字锁 → 原子名字锁 `fix(store,engine)`