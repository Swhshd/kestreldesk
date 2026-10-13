# KestrelDesk

> This file has been modified for the KestrelDesk project.

**可自托管的 AI 智能客服与实时协作平台。**

KestrelDesk 使用 Go 构建，提供客户 Web 渠道、员工工作台、AI 辅助回答和实时会话处理。

- 客户支持入口与可嵌入的 Web 渠道
- 员工会话工作台、角色权限和登录会话撤销
- 知识库检索、AI 辅助回答与人工接管
- 客户身份隔离及安全的 WebSocket 生命周期
- Docker Compose 部署，包含 MySQL 和 Qdrant

[English](README.md) · [开发指南](docs/DEVELOPMENT.md) · [安全说明](SECURITY.md)

## 界面截图

员工工作台截图使用本地合成数据。

![员工工作台](screenshots/kestreldesk/employee-workspace.jpg)

## 架构

Go 服务同时提供 Web 界面和 API。MySQL 保存关系型应用数据，Qdrant 保存检索向量。部署者自行配置模型提供商、客户渠道和邮件服务。Docker Compose 会启动应用及 MySQL/Qdrant 依赖。

## Docker 快速启动

准备 Docker Engine 和 Compose v2 插件，并确保有足够内存、磁盘构建前端与 Go 镜像。

在仓库根目录执行：

1. 复制 `.env.example` 为 `.env`。
2. 为 `MYSQL_PASSWORD`、`MYSQL_ROOT_PASSWORD` 和 `CUSTOMER_SESSION_SECRET` 设置独立随机值。数据库密码建议使用较长的随机十六进制字符串，以便安全嵌入 MySQL 连接串。
3. 首次初始化数据库前，将 `BOOTSTRAP_ADMIN_PASSWORD` 设置为独立管理员密码。
4. 校验 Compose 配置并构建启动：

```powershell
docker compose config -q
docker compose up --build -d
docker compose ps
```

打开 [http://localhost:8083](http://localhost:8083)。初始管理员用户名为 `admin`，密码是 `BOOTSTRAP_ADMIN_PASSWORD` 设置的值。全新初始化时该值不能为空。初始管理员创建完成后，从 `.env` 删除该值，并只重建应用服务：

```powershell
docker compose up -d --no-deps --force-recreate kestreldesk
```

默认 Compose 会在主机 8083 端口发布应用；Qdrant 的 REST/gRPC 端口（6333/6334）仅绑定到 `127.0.0.1` 供本机访问。自托管 Qdrant 默认没有认证和 TLS，不要把这两个端口暴露给不可信网络。需要通过网络访问时，应绑定到私有网络、启用 Qdrant API Key 认证和 TLS，并通过 HTTPS 限制应用访问。还应配置实际浏览器来源，并妥善保护 `.env` 与数据库备份。

## 配置 AI、知识库和 Web 客服

登录后按以下步骤配置：

1. 在 **Dashboard → AI Configs** 添加聊天和嵌入模型配置，并填写你自己的服务商凭证。这些凭证属于部署数据。
2. 在 **Dashboard → Knowledge** 添加知识库与内容，再在 **Dashboard → AI Agents** 中为 Agent 配置知识库。
3. 在 **Dashboard → Channels** 创建 **Web** 渠道，并配置允许嵌入的来源。
4. 使用渠道生成的 widget 或客户入口链接。支持中心地址为 `/support`。

跨域嵌入时，在挂载的配置文件中将 `server.cors.allowedOrigins` 设置为实际 HTTPS 来源。承载客户数据的界面不要使用通配来源。

## 密码恢复与邮件

本地运营者可以不依赖邮件重置管理员密码。在仓库根目录运行下列 Docker 命令，按隐藏提示输入两次新密码：

```powershell
docker compose exec kestreldesk /app/kestreldesk admin reset-password --username admin --config /app/config/config.yaml
```

使用本地构建的二进制时运行 `kestreldesk admin reset-password --username admin --config config/config.yaml`。命令不会回显密码，并会撤销该账户的活跃会话。

客户或员工邮件恢复需要配置邮件发送服务。使用 SMTP 时，可在项目 `.env` 或挂载的私有配置文件中设置参数。Compose 会将 `EMAIL_ENABLED`、`EMAIL_PROVIDER`、`EMAIL_FROM`、`SMTP_HOST`、`SMTP_PORT`、`SMTP_USER`、`SMTP_PASSWORD`、`SMTP_SECURITY`、`SMTP_USE_TLS` 和 `SMTP_TIMEOUT_SECONDS` 传给应用。将 `EMAIL_PROVIDER` 设为 `smtp`，并按邮件服务商要求将 `SMTP_SECURITY` 设为 `starttls` 或 `tls`。`SMTP_USE_TLS` 是兼容旧配置的直连 TLS 设置；未设置 `SMTP_SECURITY` 时才用于选择 TLS。新配置建议使用 `SMTP_SECURITY`。`SMTP_TIMEOUT_SECONDS` 默认为 20 秒，最大为 30 秒。不要提交填写过的 `.env` 或配置文件；`.env` 文件已从 Docker 构建上下文中排除。

## 开发与验证

可复现的前端、Go 和 Docker 命令见[开发指南](docs/DEVELOPMENT.md)。项目使用 `web/` 和 `flowgram-editor/` 下的锁文件，应使用 frozen-lockfile 安装。运行嵌入前端产物的 Go 检查前，先构建前端导出。

## 已知限制

- Windows 原生 LanceDB 开发需要 CGO、兼容编译器和匹配的原生库；目前未将该环境确立为受支持路径。
- Windows race 测试受环境限制，不能声称通过。
- 更广泛的 internal/services race 运行此前超时，不能视为通过。
- WxWork 回调时序仍是已知问题。
- 工单并发写入语义尚未定义为产品契约。
- 附件直链授权和完整端到端转人工体验仍未完成。

## 许可证

KestrelDesk 使用 Apache License 2.0 发布。许可条款和适用声明见 [LICENSE](LICENSE)。
