# 开发与构建

> FnRelay 1.0.24 · Go 1.24+ · 无第三方 Go 依赖

## 目录结构

```text
src/
  main.go          启动、CLI、生命周期
  app.go           配置、代理入口、状态、跳转
  proxy.go         单端口复用、反向代理、WebSocket
  certs.go         证书扫描、辅助证书过滤、SNI 选择
  admin.go         网关路由、管理员校验、管理 API
  backup.go        备份/恢复
  domains.go       域名后缀批量替换
  log.go           日志条目与隐私处理
  filelog.go       按天 JSONL、保留与下载
  live.go          实时请求注册表与字节统计
  web/             原生 HTML/CSS/JS 管理界面
  version.go       版本号

packaging/fnrelay/
  manifest         fnOS 应用清单
  cmd/             安装、启动、停止、卸载脚本
  config/          privilege/resource
  app/ui/          桌面入口与图标

tools/
  test_integration.py
  render_check.js
  make_test_config.py
  make_source_zip.py
  verify_fpk.py

build.py           交叉编译 + fnpack 打包
archive/           历史 fpk 与源码包
dist/              当前版本产物
docs/              使用教程与截图
```

## 环境要求

- Go **1.24+**；
- Python **3.10+**；
- 官方 `fnpack`；
- Windows 上使用仓库自带的 `tools/go` 与 `tools/fnpack.exe` 时，可直接运行 `build.py`。

`build.py` 默认查找：

```text
tools/go/bin/go.exe
tools/fnpack.exe
```

如果放在其他位置，修改 `build.py` 顶部的 `GO` / `FNPACK` 路径。

## 本地开发

```bash
cd src
go run . --data ./devdata --admin-port 18899
```

打开：

```text
http://127.0.0.1:18899/app/fnrelay
```

`--admin-port` 只用于本地开发：

- 只监听 `127.0.0.1`；
- 自动进入 dev 模式，跳过飞牛网关 Header 校验；
- 正式安装包**不会**传这个参数，管理界面只走 `${TRIM_APPDEST}/app.sock`。

开发数据目录 `devdata/` 可以随时删除，重新启动会生成默认配置。

## 测试

### 单元测试

```bash
cd src
go test ./...
```

覆盖：

- 证书扫描和辅助证书过滤；
- 域名匹配优先级；
- 跳转 URL；
- 实时请求快照；
- 日志隐私处理；
- 备份版本和格式；
- 域名后缀替换与冲突。

### 集成测试

先构建测试二进制：

```bash
cd src
go build -o ../build/fnrelay-test.exe .
```

再运行：

```bash
python tools/test_integration.py
```

测试会占用这些本地端口：

```text
18080   额外 HTTP 跳转
18081   测试上游
18443   代理端口
18899   dev 管理端口
```

覆盖：

- 单端口 HTTP/HTTPS；
- Host 外部端口跳转；
- Host 无端口默认 443；
- 精确/泛域名；
- SNI 证书选择；
- 无密码 dev 状态；
- 规则热更新；
- 自定义跳转端口；
- 额外 HTTP 跳转；
- 实时请求/字节/速度；
- 日志与脱敏；
- 备份导出、版本不一致拒绝、同版本恢复；
- 域名批量替换。

### UI 渲染检查

```bash
node tools/render_check.js
```

需要 Node.js。它用 `render_check.js` 里的最小 DOM 桩加载 `app.js`，检查各页面能否正常渲染。

## 打包

```bash
python build.py            # x86 + ARM
python build.py --arch x86 # 只打 x86
python build.py --arch arm # 只打 ARM
```

产物：

```text
dist/FnRelay-1.0.24-x86.fpk
dist/FnRelay-1.0.24-arm.fpk
dist/FnRelay-1.0.24-source.zip
dist/SHA256SUMS.txt
```

`build.py` 会：

1. `GOOS=linux CGO_ENABLED=0` 交叉编译；
2. 复制 `packaging/fnrelay`；
3. 写入二进制；
4. 调用 `fnpack` 打包；
5. 生成 SHA256。

## 版本发布检查

发布前按顺序：

```text
[ ] 更新 src/version.go
[ ] 更新 build.py 的 VERSION
[ ] 更新 packaging/fnrelay/manifest 的 version/changelog
[ ] 更新 README 和 docs 中的版本号
[ ] go test ./...
[ ] python tools/test_integration.py
[ ] node tools/render_check.js
[ ] python build.py
[ ] python tools/verify_fpk.py
[ ] 把旧版 fpk/源码包移动到 archive/<version>/
[ ] 在测试 NAS 安装并验证 443、网关、日志、备份
[ ] 提交 dist/SHA256SUMS.txt
```

> 历史 fpk 只移动、不删除。`archive/` 保留每个版本，方便回滚和对照。

## 单端口复用原理

代理监听器接受连接后读取第一个字节：

```text
0x16 → TLS ClientHello → tls.Server → HTTPS
其他 → 普通 HTTP
```

因此同一个端口可以同时处理：

- `https://域名:端口`；
- `http://域名:端口`；
- WebSocket（HTTP/1.1 Upgrade）；
- SSE / 流式响应。

客户端 ALPN 只声明 `http/1.1`，不启用 h2。

## 管理界面

- 生产环境只监听 Unix Socket：`${TRIM_APPDEST}/app.sock`；
- 飞牛网关把 `/app/fnrelay` 转发到该 socket；
- 权限读取：
  - `X-Trim-Userid`
  - `X-Trim-Isadmin`
- 修改类请求还要求 `X-FnRelay` 请求头；
- 前端为原生 HTML/CSS/JS，无构建步骤。

## 数据与日志

```text
@appdata/fnrelay/config.json
@appdata/fnrelay/certs/
@appdata/fnrelay/selfsigned.crt
@appdata/fnrelay/logs/
```

安装目标 `@appcenter/fnrelay/target` 只放程序，配置和日志不写在那里。

## 常见开发问题

### 端口被占用

本地开发默认使用 `18899`；如果被占用，换一个：

```bash
go run . --data ./devdata --admin-port 18900
```

代理端口和额外 HTTP 端口也可能冲突，改 `devdata/config.json` 或环境里的测试配置。

### 没有证书

dev 模式会使用 `devdata/selfsigned.crt` 或内置自签证书，浏览器会提示不受信任；这是正常的。

### 修改前端后没变化

前端没有构建步骤，直接刷新页面；如果被浏览器缓存，`Ctrl+F5`。

### Windows 交叉编译

`build.py` 已设置：

```text
GOOS=linux
GOARCH=amd64 / arm64
CGO_ENABLED=0
```

生成的二进制是 Linux 可执行文件，不能在 Windows 直接运行；本地调试请用 `go run .`。

## 相关文档

- [项目首页](../README.md)
- [完整使用教程](GUIDE.md)
