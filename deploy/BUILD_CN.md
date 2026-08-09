# Sub2API 编译与发布规则

本文用于构建包含管理前端的 Sub2API 单文件正式版本，并生成 Linux AMD64 发布包。

## 1. 发布原则

- 前端只能通过 `pnpm` 构建，产物固定写入 `backend/internal/web/dist/`。
- 正式后端必须使用 `-tags embed` 编译，使前端静态资源嵌入 `sub2api` 二进制；发布包不单独部署 Node.js、Vite 或前端目录。
- Linux 正式包使用 `CGO_ENABLED=0`、`GOOS=linux`、`GOARCH=amd64`。
- 备份是 PostgreSQL 逻辑备份，依赖 `pg_dump` 和 `psql`。Linux 包必须携带 `tools/postgresql/linux-amd64/`，其中的客户端工具与服务端 PostgreSQL 主版本兼容。
- 不将 `config.yaml`、密钥、`data/`、日志、数据库数据或 `node_modules` 打入发布包。

## 2. 构建前检查

在仓库根目录执行：

```bash
git diff --check
git status --short
```

构建环境要求：

- Go 版本以 `backend/go.mod` 为准。
- Node.js 与 pnpm 已安装；只能使用 pnpm 管理前端依赖。
- Linux 发布建议在 Linux 或 WSL 中构建。Windows 也可以交叉编译 Go 二进制，但打包和权限校验应在 Linux/WSL 完成。

## 3. 构建前端

准备好依赖及 `tools/postgresql/linux-amd64/` 后，推荐直接在仓库根目录执行完整发布规则：

```bash
make release-linux-amd64
```

产物输出到 `release/sub2api_<版本>_linux_amd64.tar.gz`，同时生成对应的 `.sha256` 文件。下面各节是该规则的分步说明，也可在没有 `make` 时手工执行。

从仓库根目录执行：

```bash
cd frontend
pnpm install --frozen-lockfile
pnpm run build
```

成功后确认存在 `backend/internal/web/dist/`。该目录是后端嵌入前端的唯一输入，不要将 `frontend/dist/` 作为发布目录。

## 4. 编译 Linux AMD64 正式二进制

以下命令在 Linux 或 WSL 中执行。版本号由 `backend/cmd/server/VERSION` 或当前 Git tag 决定：

```bash
cd backend
VERSION="$(./scripts/resolve-version.sh)"
COMMIT="$(git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
mkdir -p ../release/build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath \
  -tags embed \
  -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.Date=${BUILD_DATE} -X main.BuildType=custom" \
  -o ../release/build/sub2api \
  ./cmd/server
```

注意：不能省略 `-tags embed`。省略后虽然二进制可以编译，但不包含管理前端页面。

## 5. 组装正式发布包

以下示例生成 `release/sub2api_<版本>_linux_amd64.tar.gz`：

```bash
cd ..
VERSION="$(cd backend && ./scripts/resolve-version.sh)"
PACKAGE="sub2api_${VERSION}_linux_amd64"
STAGE="release/${PACKAGE}"

rm -rf "$STAGE"
mkdir -p "$STAGE/tools/postgresql/linux-amd64"

install -m 0755 release/build/sub2api "$STAGE/sub2api"
install -m 0755 deploy/start.sh "$STAGE/start.sh"
install -m 0644 deploy/config.example.yaml "$STAGE/config.example.yaml"
install -m 0644 README_CN.md "$STAGE/README_CN.md"
install -m 0644 LICENSE "$STAGE/LICENSE"
cp -a tools/postgresql/README.md "$STAGE/tools/postgresql/README.md"
cp -a tools/postgresql/linux-amd64/bin "$STAGE/tools/postgresql/linux-amd64/bin"
cp -a tools/postgresql/linux-amd64/lib "$STAGE/tools/postgresql/linux-amd64/lib"
chmod 0755 "$STAGE/tools/postgresql/linux-amd64/bin/pg_dump"
chmod 0755 "$STAGE/tools/postgresql/linux-amd64/bin/psql"

tar -C release -czf "release/${PACKAGE}.tar.gz" "$PACKAGE"
sha256sum "release/${PACKAGE}.tar.gz" > "release/${PACKAGE}.tar.gz.sha256"
```

发布目录应至少包含：

```text
sub2api
start.sh
config.example.yaml
README_CN.md
LICENSE
tools/postgresql/README.md
tools/postgresql/linux-amd64/bin/pg_dump
tools/postgresql/linux-amd64/bin/psql
tools/postgresql/linux-amd64/lib/libpq.so.5
```

`start.sh` 会默认设置 `DATA_DIR=./data`、`CONFIG_FILE=./config.yaml`、`SKIP_SETUP=true`。升级已安装系统时保留原有 `config.yaml` 和 `data/`，只替换程序及工具文件，不会重新进入 Web 安装向导。

## 6. 发布前验证

```bash
VERSION="$(cd backend && ./scripts/resolve-version.sh)"
PACKAGE="sub2api_${VERSION}_linux_amd64"

sha256sum -c "release/${PACKAGE}.tar.gz.sha256"
tar -tzf "release/${PACKAGE}.tar.gz"
tar -xOf "release/${PACKAGE}.tar.gz" "${PACKAGE}/sub2api" > /tmp/sub2api-release-check
chmod +x /tmp/sub2api-release-check
/tmp/sub2api-release-check --version
rm -f /tmp/sub2api-release-check
```

还应在目标系统或同类环境验证：

```bash
tar -xzf "release/${PACKAGE}.tar.gz" -C /tmp
/tmp/${PACKAGE}/tools/postgresql/linux-amd64/bin/pg_dump --version
/tmp/${PACKAGE}/tools/postgresql/linux-amd64/bin/psql --version
```

随包提供的 Linux x64 PostgreSQL 客户端适用于 Debian 12、Ubuntu 22.04 及其他 glibc 2.34+ 的系统。Alpine Linux 使用 musl，不可直接使用该工具目录；请执行 `apk add postgresql-client`，或使用项目 Docker 镜像中的客户端。

## 7. Windows 开发构建

Windows 本地开发仅需构建前端和运行后端。已经有 `config.yaml` 的环境按以下方式启动，避免进入安装向导：

```powershell
cd E:\dev_code_project\sub2api\frontend
pnpm install --frozen-lockfile
pnpm run build

cd ..\backend
$env:DATA_DIR = (Get-Location).Path
$env:CONFIG_FILE = (Resolve-Path .\config.yaml).Path
$env:SKIP_SETUP = "true"
go run .\cmd\server
```

前端独立调试时，后端必须先启动；Vite 的 `/setup/status` 和 `/api/v1/*` 代理连接被拒绝，说明后端未监听对应地址或端口。
