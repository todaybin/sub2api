# PostgreSQL 数据库备份工具说明

Sub2API 的数据库备份是 PostgreSQL 逻辑备份：

```text
pg_dump -> SQL 数据流 -> gzip 压缩 -> 本地 / S3 / 腾讯云 COS
```

系统不会直接复制 PostgreSQL 正在使用的数据目录。直接复制运行中的数据库文件不能保证数据一致性。

## 必需工具

- `pg_dump`：创建数据库备份。
- `psql`：恢复数据库备份。

选择本地、S3 或腾讯云 COS 只会改变备份文件的存储位置，不会取消对上述工具的依赖。

建议使用与数据库服务端相同主版本的 PostgreSQL 客户端。客户端的主版本不得低于数据库服务端主版本，否则 `pg_dump` 可能提示版本不匹配并拒绝备份。

## Linux 安装

Debian、Ubuntu：

```bash
sudo apt update
sudo apt install postgresql-client
```

RHEL、Rocky Linux、AlmaLinux、Fedora：

```bash
sudo dnf install postgresql
```

Alpine Linux：

```bash
sudo apk add postgresql-client
```

安装后检查：

```bash
pg_dump --version
psql --version
```

## Windows 安装

可以安装 PostgreSQL 官方客户端，并把完整 `bin` 目录加入系统 `PATH`。

也可以把完整客户端复制到项目目录。不能只复制 `pg_dump.exe`，因为它通常还依赖：

- `psql.exe`
- `libpq.dll`
- OpenSSL DLL
- PostgreSQL 客户端附带的其他 DLL

推荐目录：

```text
tools/postgresql/windows-amd64/bin/pg_dump.exe
tools/postgresql/windows-amd64/bin/psql.exe
tools/postgresql/windows-amd64/bin/libpq.dll
tools/postgresql/windows-amd64/bin/其他依赖 DLL
```

## 项目内便携客户端

当前项目已放入与仓库默认 `postgres:18-alpine` 同主版本的 PostgreSQL `18.4` 客户端：

```text
tools/postgresql/windows-amd64/bin/  Windows x64，EDB PostgreSQL 18.4 官方二进制包
tools/postgresql/linux-amd64/bin/    Linux x64，PGDG PostgreSQL 18.4 Debian 12 客户端包
tools/postgresql/linux-amd64/lib/    Linux x64 对应的 libpq 运行库
```

Windows 版已在本机验证 `pg_dump.exe --version` 和 `psql.exe --version`，均返回 `18.4`。Linux 版针对 Debian 12、Ubuntu 22.04 及其他使用 glibc 2.34 或更新版本的 x64 发行版。Alpine Linux 使用 musl，不能使用此处的 glibc 客户端；请在 Alpine 中执行 `apk add postgresql-client`，或使用项目 Docker 镜像内置的同主版本客户端。

支持以下目录布局：

```text
tools/postgresql/bin/...
tools/postgresql/windows-amd64/bin/...
tools/postgresql/linux-amd64/bin/...
tools/postgresql/linux-arm64/bin/...
```

后端会相对于以下位置自动查找：

1. 后端进程当前工作目录。
2. 当前工作目录的父目录。
3. `sub2api` 可执行文件所在目录。
4. 可执行文件所在目录的父目录。

Linux 便携客户端如果附带共享库，应保留与 `bin` 相邻的 `lib` 目录：

```text
tools/postgresql/linux-amd64/bin/pg_dump
tools/postgresql/linux-amd64/bin/psql
tools/postgresql/linux-amd64/lib/...
```

后端启动客户端时会自动把该 `lib` 目录加入 `LD_LIBRARY_PATH`。

Linux 下需要保证程序可执行：

```bash
chmod +x tools/postgresql/linux-amd64/bin/pg_dump
chmod +x tools/postgresql/linux-amd64/bin/psql
```

## config.yaml 配置

可以在现有 `database` 节点增加 `client_bin`，它可以指向 PostgreSQL 根目录，也可以直接指向 `bin` 目录：

```yaml
database:
  host: "数据库地址"
  port: 5432
  user: "postgres"
  password: "数据库密码"
  dbname: "sub2api"
  sslmode: "prefer"
  client_bin: "/opt/postgresql/bin"
```

Windows 示例：

```yaml
database:
  client_bin: 'E:\dev_code_project\sub2api\tools\postgresql\windows-amd64\bin'
```

也可以使用环境变量：

```text
DATABASE_CLIENT_BIN
PG_DUMP_PATH
PSQL_PATH
PG_BIN
POSTGRES_BIN
POSTGRES_HOME
```

修改配置或环境变量后只需要重启 Sub2API 后端，不需要重启 PostgreSQL。

## WSL 和 Docker

后端运行在 Windows、PostgreSQL 客户端安装在 WSL 时，系统会自动尝试通过 `wsl.exe` 执行工具。

指定 WSL 发行版：

```powershell
$env:PG_WSL_DISTRO = "Ubuntu-24.04"
```

如果客户端只存在于 PostgreSQL Docker 容器，可以指定容器名：

```powershell
$env:PG_DOCKER_CONTAINER = "sub2api-postgres"
```

系统会通过 `docker exec` 在容器内执行 `pg_dump` 或 `psql`。Docker 命令必须可用，并且运行 Sub2API 的用户必须具有操作该容器的权限。

## 远程数据库

`pg_dump` 和 `psql` 会直接使用 `config.yaml` 中的以下参数连接数据库：

- `database.host`
- `database.port`
- `database.user`
- `database.password`
- `database.dbname`
- `database.sslmode`

因此数据库可以位于本机、其他服务器、WSL 或 Docker。数据库密码通过子进程环境变量传递，不会写入备份命令行或备份记录。

## 工具查找顺序

系统按以下顺序查找客户端：

1. `PG_DUMP_PATH` 或 `PSQL_PATH` 指定的完整文件。
2. `database.client_bin` 或 `DATABASE_CLIENT_BIN`。
3. `PG_BIN` 或 `POSTGRES_BIN`。
4. `POSTGRES_HOME/bin`。
5. 项目内 `tools/postgresql/<系统>-<架构>/bin` 或 `tools/postgresql/bin`。
6. 系统 `PATH`。
7. Windows、Linux 和 macOS 的常见 PostgreSQL 安装目录。
8. Windows 下的 WSL 客户端。
9. WSL 或本机 Docker 中指定的 PostgreSQL 容器。

## 常见错误

### executable not found

说明后端没有找到 `pg_dump` 或 `psql`。安装客户端、配置 `database.client_bin`，或者按项目内目录布局放置完整客户端。

### error while loading shared libraries

Linux 便携客户端缺少共享库。复制客户端对应的完整 `lib` 目录，或使用系统包管理器安装 `postgresql-client`。

### DLL was not found

Windows 客户端依赖不完整。不要只复制 `pg_dump.exe`，应复制 PostgreSQL 完整 `bin` 目录。

### server version mismatch

客户端主版本低于数据库服务端版本。安装与服务端相同或更高主版本的 PostgreSQL 客户端。

### connection refused

检查 `database.host` 和 `database.port` 是否能从实际执行客户端的位置访问。使用 Docker 容器回退时，`localhost` 指的是该容器本身；使用 WSL 时，`localhost` 指的是 WSL 网络环境。

## 文件管理

PostgreSQL 客户端二进制、DLL 和共享库默认被 Git 忽略，不应提交到源代码仓库。部署时由管理员安装系统客户端，或单独复制到服务器的 `tools/postgresql` 目录。
