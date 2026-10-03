# LiCore Hub 端到端验证指南

本文档记录自建 Hub 分发服务的端到端验证步骤与预期结果，覆盖 `v0.4.0`
引入的 `licore hub serve` 命令。以下命令单测均可在本机（无需 root）跑通。

## 前置

- `licore` 可执行文件已构建：`go build -o licore .`
- 为隔离数据，验证使用独立的临时数据目录，不污染 `~/.licore`。

## 1. 启动 Hub 服务

```bash
# 前台启动（默认绑定 127.0.0.1:3727，Ctrl+C 优雅关闭）
./licore hub serve \
  --port 3727 \
  --data-dir /tmp/hub-e2e/server \
  --username alice --password secret
```

预期输出：

```text
已注册登录用户 alice
LiCore Hub 已就绪，监听 127.0.0.1:3727（数据目录 /tmp/hub-e2e/server）
```

服务在独立进程/终端运行后，容器端命令用 `--data-dir /tmp/hub-e2e/client`
隔离客户侧状态（镜像落地、hub 凭证）。

## 2. 登录

```bash
./licore --data-dir /tmp/hub-e2e/client login http://127.0.0.1:3727 \
  --username alice --password secret
```

预期：`已登录 http://127.0.0.1:3727 用户 alice，令牌已缓存`。

凭证落盘于 `<client>/hub/auth.json`（0600，令牌绑定到 Hub 地址）。

## 3. 准备一个测试镜像

可用任意 `.licore` 文件（`licore build` 产物或手工打包），此处用手工最小镜像：

```bash
# 简单构造一个占位 .licore（内容寻址以文件整体为 blob）
echo "licore-test-image" > /tmp/hub-e2e/demo.licore
```

## 4. 推送

```bash
./licore --data-dir /tmp/hub-e2e/client push alice/demo:v1 /tmp/hub-e2e/demo.licore \
  --hub http://127.0.0.1:3727
```

预期：`已推送 alice/demo:v1 到 http://127.0.0.1:3727`。

服务端数据目录下应出现：`blobs/sha256/<hex>`（镜像内容）与
`tags/alice/demo/v1.json`（tag 元数据）。

## 5. 搜索

```bash
./licore --data-dir /tmp/hub-e2e/client search demo --hub http://127.0.0.1:3727
```

预期输出（digest 取决于文件内容）：

```text
NAME        VERSION  DIGEST
alice/demo  v1       51113baa…
```

## 6. 拉取

```bash
./licore --data-dir /tmp/hub-e2e/client pull alice/demo:v1 --hub http://127.0.0.1:3727
```

预期：先从 Hub 下载镜像、再走本地导入链路，落盘于
`<client>/images/alice/demo/v1/`。真实 `.licore` 镜像会完成摘要校验并导入；
占位伪文件会因清单非法而在导入步骤报错——这证明下载步骤已成功、导入校验生效。

## 7. 清理

```bash
# 停止前台 hub serve（Ctrl+C），然后删除临时目录
rm -rf /tmp/hub-e2e
```

## 鉴权行为

- 除 `POST /auth/login` 外，`/tags/*`、`/blobs/*`、`/search` 均要求
  `Authorization: Bearer <jwt>`。
- 未登录或令牌无效时返回 401；登录令牌仅含有效期内读写权限。
- 未指定 `--username/--password` 启动时登录接口不可用（401），
  适合只读代理/内网只读场景。

## 已知限制

- `--storage s3` 为占位驱动，启动即明确报“尚未实现”，当前仅支持 `local`。
- JWT 密钥由进程启动时随机生成；重启后旧令牌失效，需重新登录
  （生产可后续由配置注入稳定 secret）。