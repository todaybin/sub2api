// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：在受控本地终端显式维护集成订阅授权，不自动迁移、开户或执行订阅。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, openDatabase); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type databaseOpener func(context.Context, string) (*sql.DB, error)

func run(ctx context.Context, args []string, out io.Writer, open databaseOpener) error {
	flags := flag.NewFlagSet("subscription-control-grant", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var c control.GrantCommand
	var configPath, enabled string
	var allowWrite bool
	flags.StringVar(&configPath, "config", "", "明确核对过的配置文件")
	flags.BoolVar(&allowWrite, "allow-write", false, "显式允许本次授权写入")
	flags.StringVar(&c.AppID, "app", "", "已配置集成应用标识")
	flags.Int64Var(&c.OperatorID, "operator", 0, "实际上游本地管理员")
	flags.Int64Var(&c.UserID, "user", 0, "实际计费普通用户")
	flags.Int64Var(&c.GroupID, "group", 0, "实际专属订阅分组")
	flags.StringVar(&c.ExpectedRevision, "version", "", "原授权精确版本，新建为0")
	flags.StringVar(&c.CommandID, "request-id", "", "稳定非零小写UUID")
	flags.StringVar(&enabled, "enabled", "", "必须明确true或false")
	flags.StringVar(&c.Reason, "reason", "", "本次维护理由")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("授权命令参数无效")
	}
	if !allowWrite {
		return errors.New("本命令要求显式--allow-write；未读取配置或连接数据库")
	}
	if enabled != "true" && enabled != "false" {
		return errors.New("--enabled必须明确为true或false")
	}
	c.Enabled = enabled == "true"
	if c.Validate() != nil {
		return errors.New("授权依据无效：核对应用、操作者、目标、版本、请求标识及理由")
	}
	if configPath == "" || open == nil {
		return errors.New("必须明确--config配置文件")
	}
	actual, err := filepath.Abs(configPath)
	if err != nil {
		return errors.New("无法解析配置文件路径")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := open(ctx, actual)
	if err != nil {
		return err
	}
	if db == nil {
		return errors.New("本地授权数据库不可用")
	}
	defer func() { _ = db.Close() }()
	result, err := control.NewStore(db).SaveGrant(ctx, c, service.SubscriptionControlActiveAppVerifier)
	if err != nil {
		switch {
		case errors.Is(err, control.ErrAccessDenied):
			return errors.New("当前本地管理员、集成应用或目标资源未获授权")
		case errors.Is(err, control.ErrGrantVersionConflict):
			return errors.New("授权版本冲突，请核查当前版本")
		case errors.Is(err, control.ErrCommandConflict):
			return errors.New("原请求标识已被另一组授权参数使用")
		default:
			return errors.New("授权事务未能确认提交，请使用原请求标识与参数核查")
		}
	}
	if json.NewEncoder(out).Encode(struct {
		RequestID string        `json:"requestId"`
		Grant     control.Grant `json:"grant"`
	}{c.CommandID, result}) != nil {
		return errors.New("授权已提交但结果输出失败，请使用原请求标识与参数核查")
	}
	return nil
}

func openDatabase(ctx context.Context, configPath string) (*sql.DB, error) {
	info, err := os.Stat(configPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("配置文件不存在或不是普通文件")
	}
	original, present := os.LookupEnv("CONFIG_FILE")
	if os.Setenv("CONFIG_FILE", configPath) != nil {
		return nil, errors.New("无法固定配置文件")
	}
	defer func() {
		if present {
			_ = os.Setenv("CONFIG_FILE", original)
		} else {
			_ = os.Unsetenv("CONFIG_FILE")
		}
	}()
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		return nil, errors.New("读取指定配置失败，未输出配置或凭据")
	}
	// 使用URL编码保留密码和数据库名中的特殊字符，不调用会自动迁移的初始化入口。
	u := url.URL{Scheme: "postgres", Path: "/" + cfg.Database.DBName, User: url.UserPassword(cfg.Database.User, cfg.Database.Password)}
	q := url.Values{"host": {cfg.Database.Host}, "port": {strconv.Itoa(cfg.Database.Port)}, "sslmode": {cfg.Database.SSLMode}, "TimeZone": {cfg.Timezone}, "connect_timeout": {"2"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		return nil, errors.New("打开目标数据库失败，未输出连接信息")
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(0)
	pingCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if db.PingContext(pingCtx) != nil {
		_ = db.Close()
		return nil, errors.New("连接目标数据库失败，未输出连接信息")
	}
	return db, nil
}
