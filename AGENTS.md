# AGENTS.md

sub2api = Go 后端（Gin + Ent ORM）+ Vue 3 前端（pnpm）。后端负责 API 与静态资源；前端构建产物会被后端嵌入二进制。

## Git 协作规则

本仓库基于官方 `Wei-Shaw/sub2api` 开发。官方仓库只能作为上游，不得直接向官方仓库推送自研代码。

- `upstream`：官方仓库，只用于拉取更新。
- `origin`：团队自己的 GitHub/GitLab 仓库，用于推送自研分支。
- `main`：保持与官方同步，不直接提交自研功能。
- `custom/*`：团队生产分支，包含自研功能。
- `feature/*`：单个功能开发分支。

首次配置自己的远程仓库：

```powershell
git remote rename origin upstream
git remote add origin <团队自己的仓库地址>
git fetch upstream
```

禁止执行 `git push upstream`。推送前必须确认：

```powershell
git remote -v
git branch --show-current
git status --short
```

官方更新流程：

```powershell
git fetch upstream
git switch main
git merge --ff-only upstream/main
git switch custom/main
git merge main
```

如果官方默认分支是 `master`，将命令中的 `main` 替换为 `master`。官方更新产生冲突时，只处理自研分支中的冲突，不要修改 `main` 以加入自研功能。

每个功能保持独立、可回滚的提交。不要把运行配置、密钥、数据库数据、日志或 `node_modules` 提交到 Git。

## 构建与产物

前端构建产物固定输出到 `backend/internal/web/dist/`，由 `backend/internal/web/embed_on.go` 通过 `//go:embed all:dist` 打包。

后端二进制只有在 `-tags embed` 下才编译 `embed_on.go` 并服务前端 UI。需要单文件部署时必须显式使用 `-tags embed`。

```powershell
cd frontend
pnpm install --frozen-lockfile
pnpm run build

cd ..\backend
$version = .\scripts\resolve-version.sh
go build -tags embed -ldflags="-X main.Version=$version" -o bin/server .\cmd\server
```

根 `Makefile build-backend` 不带 `-tags embed`，生成的二进制不包含前端 UI。

## 前端工具链

- 包管理只能使用 pnpm，CI 使用 `pnpm install --frozen-lockfile`。
- `frontend/pnpm-workspace.yaml` 负责声明构建脚本权限；当前 pnpm 11 配置使用 `allowBuilds`，不要删除 `esbuild` 和 `vue-demi`。
- 不要使用 npm/yarn 安装依赖。若 `node_modules` 曾由 npm 创建，先清理后使用 pnpm 安装。
- 如果 pnpm 自动大范围改写 `frontend/pnpm-lock.yaml`，确认是否只是 pnpm 版本差异，不要把无关改写混入功能提交。

## 测试

```powershell
cd backend
go test -tags=unit ./...
go test -tags=integration ./...

cd ..\frontend
pnpm exec vitest run <相关测试文件>
```

本地 Windows 没有 `make` 时直接运行原始命令。给 interface 或 struct 新增方法时，必须同步更新所有测试里的 stub/mock，否则会出现 `does not implement interface` 编译错误。

修改 `backend/ent/schema/*.go` 后必须执行并提交生成产物：

```powershell
go generate .\ent
go generate .\cmd\server
```

不要手工修改生成的 `ent/` 文件。

## 运行与配置

- 主程序入口：`backend/cmd/server/main.go`。
- 运行需要 PostgreSQL 15+ 和 Redis 7+。
- 首次启动没有 `config.yaml` 时会进入 Web 设置向导；已有配置时跳过向导。
- `config.yaml`、密钥、数据库数据和日志属于运行环境，不提交到 Git。
- 后端和前端开发目录统一使用 `E:\dev_code_project\sub2api`，不要继续在其他副本目录开发或手工同步。

## 变更边界

迁移官方更新时优先使用 Git 合并。不要整目录覆盖官方代码；特别是前端 `src/types/index.ts`、`SettingsView.vue`、Ent 生成文件和构建产物，必须先确认官方版本差异再做三方合并。

提交前检查：

```powershell
git diff --check
git status --short
git diff --stat
```
