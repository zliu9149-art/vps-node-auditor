// Command node-provision manages owner-only credentials. Every accepted write
// is backed up, applied, restarted, verified, and rolled back on failure. It
// never exposes UUIDs through list output.
// 中文：node-provision 管理所有者凭据；写操作自动备份、应用、重启、验证并在失败时
// 回滚，列表输出绝不暴露 UUID。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/provision"
	"vps-node-auditor/internal/storage"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "node-provision: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	configPath, provisionPath, command, commandArgs, err := splitGlobalArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if command == "list" {
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		user := flags.String("user", "", "optional registered user")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(commandArgs); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("list received unexpected arguments")
		}
		store, err := storage.Open(cfg.Database.Path, true)
		if err != nil {
			return err
		}
		defer store.Close()
		items, err := store.ListCredentials(ctx, *user)
		if err != nil {
			return err
		}
		if *jsonOutput {
			encoder := json.NewEncoder(output)
			encoder.SetIndent("", "  ")
			return encoder.Encode(items)
		}
		fmt.Fprintln(output, "用户\t凭据\t状态\t创建时间\t停用时间")
		for _, item := range items {
			status, revoked := "启用", "-"
			if !item.Enabled {
				status = "停用"
			}
			if item.RevokedAt != nil {
				revoked = item.RevokedAt.Format("2006-01-02 15:04:05Z07:00")
			}
			fmt.Fprintf(output, "%s\t%s\t%s\t%s\t%s\n", item.UserName, item.CredentialName,
				status, item.CreatedAt.Format("2006-01-02 15:04:05Z07:00"), revoked)
		}
		return nil
	}
	if command == "check" {
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(commandArgs); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("check accepts only --json")
		}
		provisionCfg, err := provision.LoadConfig(provisionPath)
		if err != nil {
			return err
		}
		store, err := storage.Open(cfg.Database.Path, true)
		if err != nil {
			return err
		}
		defer store.Close()
		credentials, err := store.ActiveCredentials(ctx, "")
		if err != nil {
			return err
		}
		summaries, err := store.ListCredentials(ctx, "")
		if err != nil {
			return err
		}
		health, healthErr := store.Health(ctx, time.Now())
		report := (provision.ConsistencyChecker{AuditConfig: cfg, ProvisionConfig: provisionCfg,
			Credentials: credentials, Summaries: summaries, Health: health, HealthError: healthErr}).Run(ctx)
		if *jsonOutput {
			encoder := json.NewEncoder(output)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(report); err != nil {
				return err
			}
			if report.Status == provision.CheckError {
				return errors.New("consistency check found errors")
			}
			return nil
		}
		for _, item := range report.Items {
			fmt.Fprintf(output, "[%s] %s: %s\n", strings.ToUpper(string(item.Status)), item.Name, item.Message)
		}
		if report.Status == provision.CheckError {
			return errors.New("consistency check found errors")
		}
		return nil
	}

	request, err := parseRequest(command, commandArgs)
	if err != nil {
		return err
	}
	provisionCfg, err := provision.LoadConfig(provisionPath)
	if err != nil {
		return err
	}
	store, err := storage.Open(cfg.Database.Path, false)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	result, err := provision.NewManager(provisionCfg, store).Execute(ctx, request)
	if err != nil {
		return err
	}
	if result.OutputPath != "" {
		fmt.Fprintf(output, "已完成 %s；完整 YAML 已保存到 %s。回滚点：%s\n",
			result.Operation, result.OutputPath, result.BackupPath)
	} else {
		fmt.Fprintf(output, "已完成 %s；回滚点：%s\n", result.Operation, result.BackupPath)
	}
	return nil
}

func splitGlobalArgs(args []string) (configPath, provisionPath, command string, commandArgs []string, err error) {
	configPath = "/etc/vps-node-auditor/config.json"
	provisionPath = "/etc/vps-node-auditor/provision.json"
	for len(args) > 0 {
		switch {
		case args[0] == "--config" && len(args) >= 2:
			configPath, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--config="):
			configPath, args = strings.TrimPrefix(args[0], "--config="), args[1:]
		case args[0] == "--provision-config" && len(args) >= 2:
			provisionPath, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--provision-config="):
			provisionPath, args = strings.TrimPrefix(args[0], "--provision-config="), args[1:]
		case strings.HasPrefix(args[0], "-"):
			return "", "", "", nil, errors.New("invalid global option")
		default:
			return configPath, provisionPath, args[0], args[1:], nil
		}
	}
	return "", "", "", nil, errors.New("usage: node-provision [global options] <add|list|rotate|disable|remove-user|check> [options]")
}

func parseRequest(command string, args []string) (provision.Request, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	user := flags.String("user", "", "registered user")
	credential := flags.String("credential", "", "credential display name")
	newCredential := flags.String("new-credential", "", "replacement credential display name")
	if err := flags.Parse(args); err != nil {
		return provision.Request{}, err
	}
	if *user == "" {
		return provision.Request{}, errors.New("--user is required")
	}
	request := provision.Request{UserName: *user, CredentialName: *credential,
		NewCredentialName: *newCredential}
	switch command {
	case "add":
		request.Operation = provision.OperationAdd
		if *credential == "" {
			return provision.Request{}, errors.New("add requires --credential")
		}
		if *newCredential != "" {
			return provision.Request{}, errors.New("add does not accept --new-credential")
		}
	case "rotate":
		request.Operation = provision.OperationRotate
		if *newCredential == "" {
			return provision.Request{}, errors.New("rotate requires --new-credential")
		}
	case "disable":
		request.Operation = provision.OperationDisable
		if *newCredential != "" {
			return provision.Request{}, errors.New("disable does not accept --new-credential")
		}
	case "remove-user":
		request.Operation = provision.OperationRemove
		if *credential != "" || *newCredential != "" {
			return provision.Request{}, errors.New("remove-user accepts only --user")
		}
	default:
		return provision.Request{}, fmt.Errorf("unknown command %q; use add, list, rotate, disable, or remove-user", command)
	}
	return request, nil
}
