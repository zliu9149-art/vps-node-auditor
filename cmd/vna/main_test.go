package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type recordedCall struct {
	Name string
	Args []string
}

type recordingRunner struct {
	Calls  []recordedCall
	FailAt int
	Output map[string]string
}

func (r *recordingRunner) Run(_ context.Context, stdout, _ io.Writer, name string, args ...string) error {
	r.Calls = append(r.Calls, recordedCall{Name: name, Args: append([]string(nil), args...)})
	if value := r.Output[name+" "+strings.Join(args, " ")]; value != "" {
		io.WriteString(stdout, value)
	}
	if r.FailAt > 0 && len(r.Calls) == r.FailAt {
		return errors.New("planned failure")
	}
	if name == auditBinary && len(args) > 0 && args[len(args)-1] == "health" {
		io.WriteString(stdout, "数据库：正常\n")
	}
	return nil
}

func TestInteractiveRemoveUserListsAndSelectsUser(t *testing.T) {
	runner := &recordingRunner{Output: map[string]string{
		provisionBinary + " list --json": `[{"user_name":"alice","enabled":true},{"user_name":"bob","enabled":true}]`,
	}}
	input := bufio.NewReader(strings.NewReader("2\nbob\n"))
	var output bytes.Buffer
	if err := interactiveRemoveUser(context.Background(), input, &output, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	want := []recordedCall{
		{Name: provisionBinary, Args: []string{"list", "--json"}},
		{Name: provisionBinary, Args: []string{"remove-user", "--user", "bob"}},
	}
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("calls = %#v", runner.Calls)
	}
}

func TestStatusUsesFixedReadOnlyCommandsAndStopsOnError(t *testing.T) {
	runner := &recordingRunner{}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"status"}, &output, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	want := []recordedCall{
		{Name: auditBinary, Args: []string{"--config", auditConfig, "health"}},
		{Name: "systemctl", Args: []string{"show", collectorUnit, "--property=ActiveState",
			"--property=SubState", "--property=NRestarts", "--property=MainPID",
			"--property=MemoryCurrent", "--no-pager"}},
	}
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.Calls, want)
	}
	if !strings.Contains(output.String(), "数据库：正常") {
		t.Fatalf("status output = %q", output.String())
	}

	failing := &recordingRunner{FailAt: 1}
	if err := run(context.Background(), []string{"status"}, io.Discard, io.Discard, failing); err == nil {
		t.Fatal("status ignored the first command failure")
	}
	if len(failing.Calls) != 1 {
		t.Fatalf("status continued after failure: %#v", failing.Calls)
	}
}

func TestCreateTranslatesDeviceAndExecutesDirectly(t *testing.T) {
	runner := &recordingRunner{}
	args := []string{"create", "--user", "home", "--device", "windows-laptop"}
	if err := run(context.Background(), args, io.Discard, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	want := recordedCall{Name: provisionBinary,
		Args: []string{"add", "--user", "home", "--credential", "windows-laptop"}}
	if !reflect.DeepEqual(runner.Calls, []recordedCall{want}) {
		t.Fatalf("calls = %#v, want %#v", runner.Calls, want)
	}
}

func TestRotateAndTrafficTranslateFriendlyTerms(t *testing.T) {
	runner := &recordingRunner{}
	if err := run(context.Background(), []string{"rotate", "--user=u", "--device=old", "--new-device=new", "--yes"},
		io.Discard, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"traffic", "--since", "7d", "--by-device"},
		io.Discard, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	if got := runner.Calls[0].Args; !reflect.DeepEqual(got,
		[]string{"rotate", "--user=u", "--credential=old", "--new-credential=new"}) {
		t.Fatalf("rotate args = %#v", got)
	}
	if got := runner.Calls[1].Args; !reflect.DeepEqual(got,
		[]string{"--config", auditConfig, "top", "--since", "7d", "--by-credential"}) {
		t.Fatalf("traffic args = %#v", got)
	}
}

func TestDangerousCommandsRequireConfirmation(t *testing.T) {
	runner := &recordingRunner{}
	if err := run(context.Background(), []string{"remove-user", "--user", "alice"},
		io.Discard, io.Discard, runner); err == nil {
		t.Fatal("non-interactive remove-user did not require --yes")
	}
	if len(runner.Calls) != 0 {
		t.Fatal("unconfirmed operation invoked provisioner")
	}
	if err := run(context.Background(), []string{"remove-user", "--user", "alice", "--yes"},
		io.Discard, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	want := recordedCall{Name: provisionBinary, Args: []string{"remove-user", "--user", "alice"}}
	if !reflect.DeepEqual(runner.Calls, []recordedCall{want}) {
		t.Fatalf("calls = %#v", runner.Calls)
	}
}

func TestInteractiveRemoveUserRequiresExactName(t *testing.T) {
	runner := &recordingRunner{}
	var output bytes.Buffer
	if err := runWithInput(context.Background(), []string{"remove-user", "--user", "alice"},
		strings.NewReader("wrong\n"), &output, io.Discard, runner, true); err == nil {
		t.Fatal("wrong confirmation was accepted")
	}
	if err := runWithInput(context.Background(), []string{"remove-user", "--user", "alice"},
		strings.NewReader("alice\n"), &output, io.Discard, runner, true); err != nil {
		t.Fatal(err)
	}
	if len(runner.Calls) != 1 {
		t.Fatalf("confirmed calls = %#v", runner.Calls)
	}
}

func TestTTYMenuAndNonTTYNoArgs(t *testing.T) {
	runner := &recordingRunner{}
	var output bytes.Buffer
	if err := runWithInput(context.Background(), nil, strings.NewReader("0\n"),
		&output, io.Discard, runner, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1) 创建用户配置") || len(runner.Calls) != 0 {
		t.Fatalf("menu output/calls = %q %#v", output.String(), runner.Calls)
	}
	output.Reset()
	if err := runWithInput(context.Background(), nil, strings.NewReader(""),
		&output, io.Discard, runner, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "请选择:") || !strings.Contains(output.String(), "vna create") {
		t.Fatalf("non-TTY output = %q", output.String())
	}
}

func TestMenuReportsOperationFailureAndAllowsExit(t *testing.T) {
	runner := &recordingRunner{FailAt: 1}
	var output, stderr bytes.Buffer
	if err := runWithInput(context.Background(), nil, strings.NewReader("3\n0\n"),
		&output, &stderr, runner, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "操作失败") || len(runner.Calls) != 1 {
		t.Fatalf("menu failure was not reported: stderr=%q calls=%#v", stderr.String(), runner.Calls)
	}
}

func TestLogsFollowStartsWithNoHistoryAndTimersNeverPage(t *testing.T) {
	runner := &recordingRunner{}
	if err := run(context.Background(), []string{"logs", "--follow"}, io.Discard, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"timers"}, io.Discard, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	wantLogs := []string{"--unit", collectorUnit, "--no-pager", "--follow", "--lines=0"}
	if !reflect.DeepEqual(runner.Calls[0], recordedCall{Name: "journalctl", Args: wantLogs}) {
		t.Fatalf("logs call = %#v", runner.Calls[0])
	}
	wantTimers := []string{"list-timers", "node-audit-*", "--all", "--no-pager", "--full"}
	if !reflect.DeepEqual(runner.Calls[1], recordedCall{Name: "systemctl", Args: wantTimers}) {
		t.Fatalf("timers call = %#v", runner.Calls[1])
	}
}

func TestHelpAndVersionDoNotInvokeExternalCommands(t *testing.T) {
	runner := &recordingRunner{}
	var output bytes.Buffer
	if err := run(context.Background(), nil, &output, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"version"}, &output, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("local commands invoked external tools: %#v", runner.Calls)
	}
	if !strings.Contains(output.String(), "vna status") || !strings.Contains(output.String(), "VPS Node Auditor") {
		t.Fatalf("unexpected help/version output: %q", output.String())
	}
}
