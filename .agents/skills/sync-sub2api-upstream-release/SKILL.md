---
name: sync-sub2api-upstream-release
description: 安全地将 Wei-Shaw/sub2api 官方更新合并到当前仓库的自定义生产分支，处理自研功能冲突，重新生成代码并验证构建，生成下一个自定义版本、注释标签，并且只推送到 origin。当用户要求“合并官方更新”“同步 upstream”“保留自定义计费/备份/更新功能”“发布自定义版本”“准备 custom 标签”或执行完整合并发布流程时使用。
---

# 同步 sub2api 官方更新并发布自定义版本

按顺序执行从官方上游到自定义生产分支的完整发布流程。任何安全检查或验证失败时立即停止，不得发布未完整验证的版本。

## 快速流程（优先于下方详细说明）

为缩短同步时间，按变更风险执行验证，不再默认串行执行所有重型步骤：

1. 预检和远程抓取各执行一次；不要重复 `fetch` 或重复计算标签。
2. 仅在跟踪文件阻塞切换/合并时创建一次保护 stash；不为干净工作区创建 stash。未跟踪文件原地保留。
3. 合并后仅按变更路径运行生成命令：Ent schema 变更才运行 `go generate .\\ent`，Wire/provider 变更才运行 `go generate .\\cmd\\server`，前端或依赖变更才安装依赖并构建前端。
4. 默认运行受影响包的 unit 测试和必要的前端定向测试。只有共享接口、Ent schema、迁移、认证、计费或大范围后端变更时才运行完整 unit suite；只有数据库/Redis/迁移相关变更或用户明确要求时才运行完整 integration suite。
5. 发布前始终运行一次 `go build -tags embed ./cmd/server`（使用本地缓存目录 `$env:GOCACHE`，避免 Windows 全局缓存权限问题）。不再为测试默认创建 detached worktree；只有未跟踪文件阻塞验证时才创建。
6. 合并提交、release 提交、标签和两次 `origin` 推送仍是强制步骤。任何强制检查失败都必须在提交/标签/推送前停止。

上述快速流程只减少重复和低风险验证，不改变冲突处理、用户改动保护、禁止向 `upstream` 推送、禁止强推以及发布后恢复 stash 的安全要求。

## 固定仓库规则

1. 首先读取仓库根目录的 `AGENTS.md`，始终服从其中更新的项目规则。
2. 只在当前仓库操作。除非用户明确指定其他 `custom/*` 分支，否则生产分支固定为 `custom/upstream-billing`。
3. `upstream` 只读，绝对禁止执行 `git push upstream`。
4. 只允许向 `origin` 推送指定自定义分支和本次新建的注释标签。禁止强制推送或强制覆盖标签。
5. `main` 必须保持官方历史，只能通过 `git merge --ff-only upstream/main` 更新，不能加入自研提交。
6. 不得丢弃、重置、覆盖或偷偷提交用户原有改动。不得提交 `.pnpm-store/`、`data/`、运行配置、密钥、日志、数据库、`node_modules` 或无关的 lockfile 改写。
7. 合并官方功能的同时，必须保留自定义计费、备份、更新器、设置和其他自研行为。

## 第一阶段：预检

在仓库根目录执行跨平台只读脚本。

Windows PowerShell：

```powershell
powershell -ExecutionPolicy Bypass -File .\.agents\skills\sync-sub2api-upstream-release\scripts\preflight.ps1
```

Windows 也可以直接执行：

```powershell
python .\.agents\skills\sync-sub2api-upstream-release\scripts\preflight.py
```

Linux 或 macOS：

```bash
python3 ./.agents/skills/sync-sub2api-upstream-release/scripts/preflight.py
```

检查输出的 JSON。出现以下任一情况时，在修改仓库前停止：

- `upstream` 不是 `Wei-Shaw/sub2api`；
- `origin` 缺失，或与 `upstream` 指向同一仓库；
- 本地不存在目标自定义分支；
- 无法判断官方默认分支；
- 当前正在进行 merge、rebase、cherry-pick 或 revert。

为最终报告记录当前分支与提交、远程地址、已跟踪与未跟踪改动、官方分支与提交、当前版本、建议的自定义版本与标签，以及现有 stash。

远程引用和版本计算不能依赖过期缓存。先执行：

```powershell
git fetch --prune upstream
git fetch --prune origin
git fetch origin --tags
```

网络不可用时停止，并明确说明没有执行合并或发布。

## 第二阶段：保护现有改动

如果已跟踪改动会阻止切换或合并，只对相关已跟踪路径创建名称唯一的保护 stash：

```powershell
git stash push -m "codex-pre-upstream-release-<timestamp>" -- <tracked-paths>
```

记录准确的 stash 对象。禁止使用 `git stash pop`。流程结束时使用 `git stash apply <stash-ref>` 恢复；确认恢复内容完全一致且没有冲突后，只删除本次新建的 stash。不得修改原有 stash。

未跟踪文件通常保留原位。如果某个未跟踪文件阻止切换或合并，停止并报告冲突；未经用户授权，不得删除、移动或整体暂存未跟踪内容。

## 第三阶段：更新官方主分支

从 `refs/remotes/upstream/HEAD` 判断官方默认分支是 `main` 还是 `master`，后续统一使用该名称：

```powershell
git switch <official-branch>
git merge --ff-only upstream/<official-branch>
```

确认本地官方分支与 `upstream/<official-branch>` 完全一致。快进失败时立即停止，不得通过 reset、rebase 或创建合并提交处理官方分支分叉。

## 第四阶段：合并到自定义分支

```powershell
git switch custom/upstream-billing
git merge --no-ff <official-branch>
```

发生冲突时，必须查看 merge base、官方版本、自研版本及相关调用方，并遵守以下规则：

- 保留官方功能和修复，除非已有明确且完整的替代实现；
- 保留自定义计费、备份、更新器、设置、迁移和 API 行为；
- 对重叠实现进行真正整合，不得机械地整块选择 ours 或 theirs；
- 对 `frontend/src/types/index.ts`、设置页面、Ent 生成文件和嵌入式前端产物执行三方合并；
- 禁止用任一方的整个目录覆盖另一方；
- 能通过 schema 重新生成的 Ent 代码不得手工解决。

使用 `git diff --name-only --diff-filter=U` 检查未解决冲突。提交前执行 `git diff --check`、`git status --short` 和 `git diff --stat`，审核暂存内容并确认未包含禁止路径。若无冲突合并被 Git 自动提交，不要随意 amend；验证发现必要修复时创建独立、聚焦的后续提交。

## 第五阶段：重新生成代码

比较合并范围和冲突解决内容：

- `backend/ent/schema/*.go` 发生变化时，在 `backend` 下执行 `go generate .\ent` 和 `go generate .\cmd\server`；
- Wire 输入或 provider 发生变化时，执行仓库对应生成命令，通常为 `go generate .\cmd\server`；
- 前端源码发生变化时重新构建；若 `backend/internal/web/dist/` 受版本控制，确保其与合并后的源码一致。

审核生成差异并将合理产物随合并解决提交。禁止手工编辑 Ent 生成文件。

## 第六阶段：验证合并

前端只能使用 pnpm。除非合并后的依赖声明确实要求更新，否则保持 lockfile 不变：

```powershell
cd frontend
pnpm install --frozen-lockfile
pnpm run build
cd ..\backend
go test -tags=unit ./...
go test -tags=integration ./...
```

存在相关前端测试时，执行 `pnpm exec vitest run <test-files>`。然后编译包含前端 UI 的单文件部署版本：

```powershell
$version = .\scripts\resolve-version.sh
go build -tags embed -ldflags="-X main.Version=$version" -o bin/server .\cmd\server
```

Windows 下脚本需要 Git Bash 时，使用仓库支持的等价调用。不得用根目录的 `Makefile build-backend` 代替，因为它不包含 `-tags embed`。

任何必需的生成、测试、前端构建或嵌入式后端构建失败时，都必须在 release commit、打标签和推送之前停止，报告准确错误，并保持分支可供检查。

## 第七阶段：完成合并提交

如果合并仍处于待提交状态且验证通过，使用官方版本创建合并提交：

```text
Merge upstream vX.Y.Z into custom/upstream-billing
```

冲突解决和生成产物放入该合并提交。若 Git 已自动创建合并提交而验证需要修复，只单独提交必要修复。不得修改已经推送的历史。

## 第八阶段：准备自定义版本

再次获取 `origin` 标签。从合并后的官方版本来源得到基础版本 `X.Y.Z`。同时检查本地和 `origin` 上的 `vX.Y.Z-custom.*` 标签；令 `N = 已有最大 N + 1`，不存在时从 `1` 开始：

```text
版本：X.Y.Z-custom.N
标签：vX.Y.Z-custom.N
```

建议标签已存在于本地或 `origin` 时拒绝继续。把 `backend/cmd/server/VERSION` 更新为不带前导 `v` 的版本，然后创建独立 release commit 和注释标签：

```powershell
git add backend/cmd/server/VERSION
git commit -m "chore(release): prepare vX.Y.Z-custom.N"
git tag -a vX.Y.Z-custom.N -m "vX.Y.Z-custom.N"
```

确认注释标签指向 release commit，并且该提交没有混入用户无关文件。

## 第九阶段：只发布到 origin

推送前必须复核 `git remote -v`、`git branch --show-current`、`git status --short`、`git log -3 --oneline --decorate` 和 `git show --stat --oneline vX.Y.Z-custom.N`。

当前分支必须是指定的 `custom/*`，`origin` 必须是团队仓库，`upstream` 必须仍是官方仓库。先推送分支，再推送准确标签：

```powershell
git push origin custom/upstream-billing
git push origin vX.Y.Z-custom.N
```

禁止使用 `--force`、`--force-with-lease`、`--mirror` 或 `--tags`。分支推送失败时不得推送标签。分支成功但标签失败时，明确报告部分发布状态；解决原因后只重试该准确标签。

## 第十阶段：恢复并核验

使用 `git stash apply <recorded-stash-ref>` 恢复本次保护的改动。确认原始差异恢复且没有冲突后，只删除本次创建的 stash；不得删除旧 stash。

只读核验远程状态：

```powershell
git ls-remote --heads origin custom/upstream-billing
git ls-remote --tags origin vX.Y.Z-custom.N vX.Y.Z-custom.N^{}
git status --short
git stash list
```

最终报告必须列出官方提交、合并提交、release commit、版本、注释标签、验证命令与结果、推送的远程和分支、恢复的用户改动、剩余未跟踪文件与 stash，并明确说明没有向 `upstream` 推送任何内容。

## 失败处理规则

- 必需命令失败时不得宣称成功；
- 未完整验证的提交不得打标签；
- 有未解决冲突或混入无关用户文件时不得推送；
- 不得通过破坏性清理让测试通过；
- 本地标签已创建但尚未推送时，保留供检查，除非用户明确要求删除或重建；
- 远程分支被并发更新时重新 fetch 并评估，绝不覆盖远程历史。
