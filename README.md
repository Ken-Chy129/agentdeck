# AgentDeck

用于跨机器管理编程智能体环境的个人控制平面。目前支持技能管理，后续将支持 MCP 服务器、
CLI 配置和凭据管理：在 VPS 上运行一个 Web 控制台，并在每台机器上安装轻量的
`agentdeck` CLI。由机器主动拉取数据，服务器无需主动连接机器。

```
浏览器 ──▶ agentdeckd（Go、SQLite、内嵌 Web UI） ◀── HTTPS ── agentdeck sync（每台机器）
              技能 · 版本 · 机器 · 分配关系 · 任务 · 同步日志
```

## 功能

- **技能库**：支持版本管理和内容寻址的技能包。可以在浏览器中创建或编辑、上传 tar.gz，
  也可以在任意机器上执行 `agentdeck push ~/.agents/skills/foo`。
- **分发**：通过单台机器页面或矩阵视图将技能分配给机器。`agentdeck sync` 会执行安装、
  更新或删除，使 `~/.agents/skills/` 与服务器保持一致，然后创建符号链接到
  `~/.claude/skills/`。发生冲突时以服务器为准；本地修改会先备份至
  `~/.agents/skills/.agentdeck-backup/`。未分配的技能不会被改动。
- **CLI 清单**：每次同步都会上报 node/npm/brew/go/… 的版本，以及智能体 CLI
  （claude、codex、gemini 等）的安装来源和路径。控制台会与 npm 上的版本进行比较，
  标出需要升级的项目，并提供可复制的升级命令。
- **任务**：可以为机器加入 `npm_upgrade` / `brew_upgrade` 升级任务；任务会在下次同步时
  执行并回传输出。任务类型采用白名单机制，参数由客户端校验。

## 服务端

```sh
docker compose -f deploy/docker-compose.yml up -d --build   # 监听 127.0.0.1:8480
cat /opt/agentdeck/data/admin_token                          # 将令牌粘贴到 Web 登录页
```

请通过 Caddy/nginx 配置 TLS 反向代理（参见 `deploy/Caddyfile.snippet`）。可用环境变量：
`AGENTDECK_ADDR`、`AGENTDECK_DATA`、`AGENTDECK_ADMIN_TOKEN`（可选；未设置时会自动生成并写入
`data/admin_token`）。

## 客户端机器

```sh
go install github.com/Ken-Chy129/agentdeck/cmd/agentdeck@latest   # 也可以使用 dist/ 中的二进制文件
agentdeck login https://deck.example.com <enroll-token> --name mbp
agentdeck sync                     # 手动同步一次
agentdeck install-schedule --watch # 保持在线：数秒内执行控制台命令
agentdeck install-schedule         # 或仅使用定时器：每 15 分钟同步一次
agentdeck push ~/.agents/skills/my-skill --note "tweak"
agentdeck status
agentdeck inventory
```

`--watch` 会让 CLI 常驻并长轮询服务器，因此在控制台的「终端」标签页中输入的命令
（或通过升级按钮触发的命令）通常会在数秒内执行。机器始终主动连接服务器，因此即使服务器
无法直接访问机器也能正常工作。在 Linux 上，请执行 `loginctl enable-linger $USER`，
以确保退出登录后服务仍能继续运行。

配置文件：`~/.config/agentdeck/config.json`（权限为 0600）。锁文件：
`~/.agents/skills/.agentdeck-lock.json`。

## 目录结构

```
cmd/agentdeckd        服务端入口
cmd/agentdeck         机器端 CLI
internal/api          HTTP 处理器（/api/admin/* 使用管理员令牌，/api/agent/* 使用机器令牌）
internal/store        SQLite 数据库结构与查询
internal/bundle       tar.gz 打包/解包、稳定摘要、安全目录替换
internal/inventory    版本信息收集（仅名称、版本和路径，不收集环境变量或配置内容）
internal/sync         客户端：配置、锁文件、协调循环、符号链接、任务白名单
internal/protocol     两端共用的 JSON 数据结构
web/static            单页控制台（原生 JS，内嵌到二进制文件中）
deploy/               Dockerfile、Compose 配置和 Caddy 配置片段
```
