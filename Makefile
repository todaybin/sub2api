.PHONY: build build-backend build-frontend release-linux-amd64 check-linux-amd64-backup-tools test test-backend test-frontend test-frontend-critical

VERSION ?= $(shell cd backend && ./scripts/resolve-version.sh)
GIT_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_NAME := sub2api_$(VERSION)_linux_amd64
RELEASE_STAGE := release/$(RELEASE_NAME)
LINUX_AMD64_PG_TOOLS := tools/postgresql/linux-amd64

FRONTEND_CRITICAL_VITEST := \
	src/i18n/__tests__/localeKeyCompleteness.spec.ts \
	src/api/__tests__/client.spec.ts \
	src/api/__tests__/tokenRefresh.spec.ts \
	src/api/__tests__/channelMonitorV2.spec.ts \
	src/views/auth/__tests__/LinuxDoCallbackView.spec.ts \
	src/views/auth/__tests__/WechatCallbackView.spec.ts \
	src/views/user/__tests__/PaymentView.spec.ts \
	src/views/user/__tests__/PaymentResultView.spec.ts \
	src/views/user/__tests__/ChannelStatusView.mode.spec.ts \
	src/components/user/profile/__tests__/ProfileInfoCard.spec.ts \
	src/views/admin/__tests__/SettingsView.spec.ts \
	src/features/channel-monitor-v2/__tests__/designSystem.structure.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorFormat.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorZoom.spec.ts

# 一键编译前后端
build: build-backend build-frontend

# 编译后端（复用 backend/Makefile）
build-backend:
	@$(MAKE) -C backend build

# 编译前端（需要已安装依赖）
build-frontend:
	@pnpm --dir frontend run build

# Build the frontend first, embed it in the Linux binary, and package backup tools.
release-linux-amd64: build-frontend check-linux-amd64-backup-tools
	@rm -rf "$(RELEASE_STAGE)"
	@mkdir -p "$(RELEASE_STAGE)/tools/postgresql/linux-amd64"
	@cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
		-trimpath \
		-tags embed \
		-ldflags="-s -w -X main.Version=$(VERSION) -X main.Commit=$(GIT_COMMIT) -X main.Date=$(BUILD_DATE) -X main.BuildType=custom" \
		-o "../$(RELEASE_STAGE)/sub2api" \
		./cmd/server
	@install -m 0755 deploy/start.sh "$(RELEASE_STAGE)/start.sh"
	@install -m 0644 deploy/config.example.yaml "$(RELEASE_STAGE)/config.example.yaml"
	@install -m 0644 README_CN.md "$(RELEASE_STAGE)/README_CN.md"
	@install -m 0644 LICENSE "$(RELEASE_STAGE)/LICENSE"
	@install -m 0644 tools/postgresql/README.md "$(RELEASE_STAGE)/tools/postgresql/README.md"
	@cp -a "$(LINUX_AMD64_PG_TOOLS)/bin" "$(RELEASE_STAGE)/tools/postgresql/linux-amd64/bin"
	@cp -a "$(LINUX_AMD64_PG_TOOLS)/lib" "$(RELEASE_STAGE)/tools/postgresql/linux-amd64/lib"
	@chmod 0755 "$(RELEASE_STAGE)/sub2api" \
		"$(RELEASE_STAGE)/tools/postgresql/linux-amd64/bin/pg_dump" \
		"$(RELEASE_STAGE)/tools/postgresql/linux-amd64/bin/psql"
	@tar -C release -czf "release/$(RELEASE_NAME).tar.gz" "$(RELEASE_NAME)"
	@sha256sum "release/$(RELEASE_NAME).tar.gz" > "release/$(RELEASE_NAME).tar.gz.sha256"
	@echo "Release: release/$(RELEASE_NAME).tar.gz"

check-linux-amd64-backup-tools:
	@test -x "$(LINUX_AMD64_PG_TOOLS)/bin/pg_dump" || \
		(echo "Missing executable: $(LINUX_AMD64_PG_TOOLS)/bin/pg_dump" >&2; exit 1)
	@test -x "$(LINUX_AMD64_PG_TOOLS)/bin/psql" || \
		(echo "Missing executable: $(LINUX_AMD64_PG_TOOLS)/bin/psql" >&2; exit 1)
	@test -f "$(LINUX_AMD64_PG_TOOLS)/lib/libpq.so.5" || \
		(echo "Missing library: $(LINUX_AMD64_PG_TOOLS)/lib/libpq.so.5" >&2; exit 1)

# 运行测试（后端 + 前端）
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test

test-frontend:
	@pnpm --dir frontend run lint:check
	@pnpm --dir frontend run typecheck
	@$(MAKE) test-frontend-critical

test-frontend-critical:
	@pnpm --dir frontend exec vitest run $(FRONTEND_CRITICAL_VITEST)
