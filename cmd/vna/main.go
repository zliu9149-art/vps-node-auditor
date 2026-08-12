// Command vna is the owner-only administration entrypoint for VPS Node Auditor.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	auditBinary     = "/usr/local/libexec/vps-node-auditor/node-audit"
	provisionBinary = "/usr/local/libexec/vps-node-auditor/node-provision"
	backupBinary    = "/usr/local/sbin/node-audit-backup"
	restoreBinary   = "/usr/local/sbin/node-audit-restore"
	auditConfig     = "/etc/vps-node-auditor/config.json"
	collectorUnit   = "node-audit-collector.service"
)

var version = "dev"

type commandRunner interface {
	Run(context.Context, io.Writer, io.Writer, string, ...string) error
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func main() {
	interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	if err := runWithInput(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		execCommandRunner{}, interactive); err != nil {
		fmt.Fprintf(os.Stderr, "vna: %v\n", err)
		os.Exit(1)
	}
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, runner commandRunner) error {
	return runWithInput(ctx, args, strings.NewReader(""), stdout, stderr, runner, false)
}

func runWithInput(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer,
	runner commandRunner, interactive bool) error {
	if len(args) == 0 {
		if !interactive {
			writeHelp(stdout)
			return nil
		}
		return runMenu(ctx, bufio.NewReader(input), stdout, stderr, runner)
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		writeHelp(stdout)
		return nil
	}

	command, commandArgs := args[0], args[1:]
	switch command {
	case "version":
		if len(commandArgs) != 0 {
			return errors.New("version 不接受参数")
		}
		fmt.Fprintf(stdout, "VPS Node Auditor %s\n", version)
		return nil
	case "status":
		if len(commandArgs) != 0 {
			return errors.New("status 不接受参数")
		}
		fmt.Fprintln(stdout, "== 审计健康 ==")
		if err := runner.Run(ctx, stdout, stderr, auditBinary, "--config", auditConfig, "health"); err != nil {
			return fmt.Errorf("读取审计健康状态: %w", err)
		}
		fmt.Fprintln(stdout, "\n== 后台采集器 ==")
		return delegate(ctx, runner, stdout, stderr, "systemctl", "show", collectorUnit,
			"--property=ActiveState", "--property=SubState", "--property=NRestarts",
			"--property=MainPID", "--property=MemoryCurrent", "--no-pager")
	case "check":
		return delegate(ctx, runner, stdout, stderr, provisionBinary, append([]string{"check"}, commandArgs...)...)
	case "users":
		return delegate(ctx, runner, stdout, stderr, provisionBinary, append([]string{"list"}, commandArgs...)...)
	case "create":
		translated := translateOptions(commandArgs, map[string]string{"--device": "--credential"})
		return delegate(ctx, runner, stdout, stderr, provisionBinary, append([]string{"add"}, translated...)...)
	case "rotate", "disable", "remove-user":
		if command == "remove-user" && optionValue(commandArgs, "--user") == "" && interactive {
			return interactiveRemoveUser(ctx, bufio.NewReader(input), stdout, stderr, runner)
		}
		clean, yes := removeYes(commandArgs)
		translated := translateOptions(clean, map[string]string{
			"--device": "--credential", "--new-device": "--new-credential",
		})
		if err := confirmDangerous(command, translated, input, stdout, interactive, yes); err != nil {
			return err
		}
		return delegate(ctx, runner, stdout, stderr, provisionBinary, append([]string{command}, translated...)...)
	case "traffic":
		translated := translateOptions(commandArgs, map[string]string{"--by-device": "--by-credential"})
		return delegate(ctx, runner, stdout, stderr, auditBinary,
			append([]string{"--config", auditConfig, "top"}, translated...)...)
	case "user":
		return delegate(ctx, runner, stdout, stderr, auditBinary,
			append([]string{"--config", auditConfig, "user"}, commandArgs...)...)
	case "month":
		return delegate(ctx, runner, stdout, stderr, auditBinary,
			append([]string{"--config", auditConfig, "month"}, commandArgs...)...)
	case "logs":
		return runLogs(ctx, commandArgs, stdout, stderr, runner)
	case "timers":
		if len(commandArgs) != 0 {
			return errors.New("timers 不接受参数")
		}
		return delegate(ctx, runner, stdout, stderr, "systemctl", "list-timers",
			"node-audit-*", "--all", "--no-pager", "--full")
	case "backup":
		if len(commandArgs) != 0 {
			return errors.New("backup 不接受参数")
		}
		return delegate(ctx, runner, stdout, stderr, backupBinary)
	case "restore":
		clean, yes := removeYes(commandArgs)
		if len(clean) != 1 {
			return errors.New("restore 需要一个备份文件路径")
		}
		if !yes {
			if !interactive {
				return errors.New("非交互恢复必须添加 --yes")
			}
			if !confirmLine(input, stdout, "输入 RESTORE 确认恢复数据库: ", "RESTORE") {
				return errors.New("操作已取消")
			}
		}
		return delegate(ctx, runner, stdout, stderr, restoreBinary, clean[0])
	default:
		return fmt.Errorf("未知命令 %q；运行 vna help 查看用法", command)
	}
}

func confirmDangerous(command string, args []string, input io.Reader, output io.Writer,
	interactive, yes bool) error {
	if yes {
		return nil
	}
	if !interactive {
		return fmt.Errorf("非交互执行 %s 必须添加 --yes", command)
	}
	if command == "remove-user" {
		user := optionValue(args, "--user")
		if user == "" {
			return errors.New("remove-user 需要 --user")
		}
		if !confirmLine(input, output, "输入完整用户名 "+user+" 确认撤销全部连接: ", user) {
			return errors.New("操作已取消")
		}
		return nil
	}
	if !confirmLine(input, output, "输入 yes 确认执行 "+command+": ", "yes") {
		return errors.New("操作已取消")
	}
	return nil
}

func confirmLine(input io.Reader, output io.Writer, prompt, expected string) bool {
	fmt.Fprint(output, prompt)
	reader, ok := input.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(input)
	}
	line, err := reader.ReadString('\n')
	return (err == nil || len(line) > 0) && strings.TrimSpace(line) == expected
}

func removeYes(args []string) ([]string, bool) {
	clean := make([]string, 0, len(args))
	yes := false
	for _, argument := range args {
		if argument == "--yes" {
			yes = true
			continue
		}
		clean = append(clean, argument)
	}
	return clean, yes
}

func optionValue(args []string, name string) string {
	for index, argument := range args {
		if strings.HasPrefix(argument, name+"=") {
			return strings.TrimPrefix(argument, name+"=")
		}
		if argument == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func runMenu(ctx context.Context, reader *bufio.Reader, stdout, stderr io.Writer, runner commandRunner) error {
	for {
		fmt.Fprint(stdout, `
VPS Node Auditor
1) 创建用户配置
2) 管理用户和设备
3) 查看流量统计
4) 节点状态与一致性检查
5) 查看运行日志
6) 备份与恢复
0) 退出
请选择: `)
		choice, err := reader.ReadString('\n')
		if err != nil && len(choice) == 0 {
			return err
		}
		switch strings.TrimSpace(choice) {
		case "0":
			return nil
		case "1":
			user := prompt(reader, stdout, "用户名: ")
			device := prompt(reader, stdout, "设备名: ")
			if user != "" && device != "" {
				if err := runWithInput(ctx, []string{"create", "--user", user, "--device", device}, reader, stdout, stderr, runner, true); err != nil {
					fmt.Fprintf(stderr, "操作失败: %v\n", err)
				}
			}
		case "2":
			if err := runUserMenu(ctx, reader, stdout, stderr, runner); err != nil {
				fmt.Fprintf(stderr, "操作失败: %v\n", err)
			}
		case "3":
			if err := runWithInput(ctx, []string{"traffic"}, reader, stdout, stderr, runner, true); err != nil {
				fmt.Fprintf(stderr, "操作失败: %v\n", err)
			}
		case "4":
			fmt.Fprint(stdout, "1) 状态摘要\n2) 完整一致性检查\n请选择: ")
			sub := prompt(reader, stdout, "")
			command := "status"
			if sub == "2" {
				command = "check"
			}
			if err := runWithInput(ctx, []string{command}, reader, stdout, stderr, runner, true); err != nil {
				fmt.Fprintf(stderr, "操作失败: %v\n", err)
			}
		case "5":
			if err := runWithInput(ctx, []string{"logs"}, reader, stdout, stderr, runner, true); err != nil {
				fmt.Fprintf(stderr, "操作失败: %v\n", err)
			}
		case "6":
			fmt.Fprint(stdout, "1) 立即备份\n2) 从备份恢复\n请选择: ")
			sub := prompt(reader, stdout, "")
			if sub == "1" {
				if err := runWithInput(ctx, []string{"backup"}, reader, stdout, stderr, runner, true); err != nil {
					fmt.Fprintf(stderr, "操作失败: %v\n", err)
				}
			} else if sub == "2" {
				path := prompt(reader, stdout, "备份文件路径: ")
				if err := runWithInput(ctx, []string{"restore", path}, reader, stdout, stderr, runner, true); err != nil {
					fmt.Fprintf(stderr, "操作失败: %v\n", err)
				}
			}
		default:
			fmt.Fprintln(stderr, "无效编号，请重新选择。")
		}
	}
}

func runUserMenu(ctx context.Context, reader *bufio.Reader, stdout, stderr io.Writer, runner commandRunner) error {
	fmt.Fprint(stdout, `1) 用户和设备列表
2) 增加设备
3) 轮换设备凭据
4) 撤销设备
5) 撤销用户全部连接
6) 查看用户流量
请选择: `)
	switch prompt(reader, stdout, "") {
	case "1":
		return runWithInput(ctx, []string{"users"}, reader, stdout, stderr, runner, true)
	case "2":
		return runWithInput(ctx, []string{"create", "--user", prompt(reader, stdout, "用户名: "),
			"--device", prompt(reader, stdout, "设备名: ")}, reader, stdout, stderr, runner, true)
	case "3":
		return runWithInput(ctx, []string{"rotate", "--user", prompt(reader, stdout, "用户名: "),
			"--device", prompt(reader, stdout, "旧设备名: "), "--new-device", prompt(reader, stdout, "新设备名: ")},
			reader, stdout, stderr, runner, true)
	case "4":
		return runWithInput(ctx, []string{"disable", "--user", prompt(reader, stdout, "用户名: "),
			"--device", prompt(reader, stdout, "设备名: ")}, reader, stdout, stderr, runner, true)
	case "5":
		return interactiveRemoveUser(ctx, reader, stdout, stderr, runner)
	case "6":
		return runWithInput(ctx, []string{"user", prompt(reader, stdout, "用户名: ")}, reader, stdout, stderr, runner, true)
	default:
		return errors.New("无效编号")
	}
}

func interactiveRemoveUser(ctx context.Context, reader *bufio.Reader, stdout, stderr io.Writer,
	runner commandRunner) error {
	var listing strings.Builder
	if err := runner.Run(ctx, &listing, stderr, provisionBinary, "list", "--json"); err != nil {
		return fmt.Errorf("读取用户列表: %w", err)
	}
	var credentials []struct {
		UserName string `json:"user_name"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(listing.String()), &credentials); err != nil {
		return errors.New("解析用户列表失败")
	}
	seen := make(map[string]struct{})
	var users []string
	for _, credential := range credentials {
		if !credential.Enabled {
			continue
		}
		if _, exists := seen[credential.UserName]; exists {
			continue
		}
		seen[credential.UserName] = struct{}{}
		users = append(users, credential.UserName)
	}
	if len(users) == 0 {
		return errors.New("没有可撤销的活动用户")
	}
	for index, user := range users {
		fmt.Fprintf(stdout, "%d) %s\n", index+1, user)
	}
	choice := prompt(reader, stdout, "请输入用户编号: ")
	index, err := strconv.Atoi(choice)
	if err != nil || index < 1 || index > len(users) {
		return errors.New("无效用户编号")
	}
	return runWithInput(ctx, []string{"remove-user", "--user", users[index-1]},
		reader, stdout, stderr, runner, true)
}

func prompt(reader *bufio.Reader, output io.Writer, label string) string {
	fmt.Fprint(output, label)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func runLogs(ctx context.Context, args []string, stdout, stderr io.Writer, runner commandRunner) error {
	flags := flag.NewFlagSet("logs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	follow := flags.Bool("follow", false, "只跟踪启动后的新日志")
	since := flags.String("since", "10min", "回看时间")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.ContainsAny(*since, "\r\n") || *since == "" {
		return errors.New("logs 参数无效")
	}
	journalArgs := []string{"--unit", collectorUnit, "--no-pager"}
	if *follow {
		journalArgs = append(journalArgs, "--follow", "--lines=0")
	} else {
		journalArgs = append(journalArgs, "--since=-"+strings.TrimPrefix(*since, "-"))
	}
	return delegate(ctx, runner, stdout, stderr, "journalctl", journalArgs...)
}

func delegate(ctx context.Context, runner commandRunner, stdout, stderr io.Writer, name string, args ...string) error {
	if err := runner.Run(ctx, stdout, stderr, name, args...); err != nil {
		return fmt.Errorf("执行 %s: %w", filepathBase(name), err)
	}
	return nil
}

func translateOptions(args []string, replacements map[string]string) []string {
	translated := make([]string, 0, len(args))
	for _, argument := range args {
		replaced := argument
		for source, destination := range replacements {
			if argument == source {
				replaced = destination
				break
			}
			if strings.HasPrefix(argument, source+"=") {
				replaced = destination + strings.TrimPrefix(argument, source)
				break
			}
		}
		translated = append(translated, replaced)
	}
	return translated
}

func filepathBase(path string) string {
	if index := strings.LastIndexAny(path, "/\\"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func writeHelp(output io.Writer) {
	fmt.Fprint(output, `VPS Node Auditor (vna)
用法：
  sudo vna                              打开中文交互菜单
  vna create --user NAME --device NAME
  vna users [--user NAME] [--json]
  vna rotate --user NAME --device OLD --new-device NEW [--yes]
  vna disable --user NAME --device NAME [--yes]
  vna remove-user --user NAME [--yes]
  vna traffic [--since 24h] [--by-device] [--json]
  vna user NAME [--since 7d] [--bucket auto] [--json]
  vna month [--json]
  vna status | check [--json]
  vna logs [--since 10min] | logs --follow
  vna timers | backup | restore FILE [--yes] | version

非交互执行 rotate、disable、remove-user 或 restore 时必须添加 --yes。
`)
}
