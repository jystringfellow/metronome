package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
)

func TestMain(m *testing.M) {
	if os.Getenv("METRONOME_TEST_HELPER") == "1" {
		code, err := runHelper(os.Args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(99)
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

type helperEnvironment struct {
	Vars  map[string]string
	Brief string
}

func runHelper(args []string) (int, error) {
	for len(args) > 0 {
		switch args[0] {
		case "env":
			vars := make(map[string]string)
			for _, entry := range os.Environ() {
				key, value, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(key, "METRONOME_") {
					vars[key] = value
				}
			}
			brief, err := os.ReadFile(vars["METRONOME_BRIEF"])
			if err != nil {
				return 0, err
			}
			data, err := json.Marshal(helperEnvironment{Vars: vars, Brief: string(brief)})
			if err != nil {
				return 0, err
			}
			if err := os.WriteFile(args[1], data, 0o600); err != nil {
				return 0, err
			}
			args = args[2:]
		case "branch":
			if err := os.WriteFile(os.Getenv("METRONOME_BRANCH_FILE"), []byte(args[1]+"\n"), 0o600); err != nil {
				return 0, err
			}
			args = args[2:]
		case "file-output":
			for _, file := range []*os.File{os.Stdout, os.Stderr} {
				info, err := file.Stat()
				if err != nil || !info.Mode().IsRegular() {
					return 0, fmt.Errorf("runner output is not a direct file")
				}
			}
			args = args[1:]
		case "remove":
			if err := os.Remove(os.Getenv("METRONOME_BRANCH_FILE")); err != nil {
				return 0, err
			}
			args = args[1:]
		case "exit":
			return strconv.Atoi(args[1])
		case "stdout":
			fmt.Fprintln(os.Stdout, strings.Join(args[1:], " "))
			return 0, nil
		case "wait":
			if err := os.WriteFile(args[1], nil, 0o600); err != nil {
				return 0, err
			}
			deadline := time.Now().Add(15 * time.Second)
			for !fileExists(args[2]) {
				if time.Now().After(deadline) {
					return 0, fmt.Errorf("timed out waiting for release")
				}
				time.Sleep(5 * time.Millisecond)
			}
			args = args[3:]
		case "trap":
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(signals)
			if err := os.WriteFile(args[1], nil, 0o600); err != nil {
				return 0, err
			}
			select {
			case sig := <-signals:
				return 7, os.WriteFile(args[2], []byte(sig.String()), 0o600)
			case <-time.After(15 * time.Second):
				return 0, fmt.Errorf("timed out waiting for signal")
			}
		default:
			return 0, fmt.Errorf("unknown helper verb %q", args[0])
		}
	}
	return 0, nil
}

func helper(t *testing.T, args ...string) []string {
	t.Helper()
	t.Setenv("METRONOME_TEST_HELPER", "1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	return append([]string{os.Args[0]}, args...)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type runOutputBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (buf *runOutputBuffer) Write(data []byte) (int, error) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return buf.data.Write(data)
}

func (buf *runOutputBuffer) String() string {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return buf.data.String()
}

type runningCommand struct {
	out, errOut runOutputBuffer
	code        int
	done        chan struct{}
	cancel      context.CancelFunc
}

func startRun(e *env, args ...string) *runningCommand {
	e.t.Helper()
	return startRunWithTerminal(e, false, args...)
}

func startRunWithTerminal(e *env, terminal bool, args ...string) *runningCommand {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	command := &runningCommand{done: make(chan struct{}), cancel: cancel}
	e.t.Cleanup(func() {
		cancel()
		<-command.done
	})
	go func() {
		defer close(command.done)
		deps := e.deps()
		deps.Terminal = func() bool { return terminal }
		command.code = cli.RunWithDeps(ctx, append([]string{"--server", "http://metronome.test", "--as", "laptop"}, args...), &command.out, &command.errOut, deps)
	}()
	return command
}

func (cmd *runningCommand) expectExit(t *testing.T, code int) {
	t.Helper()
	select {
	case <-cmd.done:
		if cmd.code != code {
			t.Fatalf("run exited %d, want %d: %s%s", cmd.code, code, cmd.out.String(), cmd.errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("run did not finish: %s", cmd.errOut.String())
	}
}

type runEvent struct {
	kind string
	data struct {
		RunID      string    `json:"run_id"`
		Reason     string    `json:"reason"`
		Branch     string    `json:"branch"`
		LeaseUntil time.Time `json:"lease_until"`
	}
}

func runEvents(e *env) []runEvent {
	e.t.Helper()
	var events []runEvent
	for _, line := range strings.Split(strings.TrimSpace(e.ok("pat", "events")), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) != 5 {
			e.t.Fatalf("invalid event line: %q", line)
		}
		event := runEvent{kind: parts[3]}
		if err := json.Unmarshal([]byte(parts[4]), &event.data); err != nil {
			e.t.Fatal(err)
		}
		if event.data.RunID == "R1" {
			events = append(events, event)
		}
	}
	return events
}

func assertRunClosed(e *env, reason, branch string) {
	e.t.Helper()
	var closed bool
	for _, event := range runEvents(e) {
		if closed && (event.kind == flow.EvHeartbeat || event.kind == flow.EvRunClosed) {
			e.t.Fatalf("unexpected event after run closed: %+v", event)
		}
		if event.kind == flow.EvRunClosed {
			closed = true
			if event.data.Reason != reason || event.data.Branch != branch {
				e.t.Fatalf("closed: %+v, want %s on %q", event.data, reason, branch)
			}
		}
	}
	if !closed {
		e.t.Fatal("run was not closed")
	}
	if len(e.board().Runs) != 0 {
		e.t.Fatal("run still open")
	}
}

func TestRunClosesOnCommandExit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		branch string
	}{
		{"success", []string{"branch", " metronome/I1.1 ", "stdout", "--flag", "--"}, 0, "metronome/I1.1"},
		{"failure", []string{"branch", "metronome/I1.1", "exit", "3"}, 3, ""},
		{"no branch", []string{"exit", "0"}, 0, ""},
		{"whitespace branch", []string{"branch", " \t"}, 0, ""},
		{"missing branch file", []string{"remove"}, 0, ""},
		{"multiline branch", []string{"branch", "first\nsecond"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBriefEnv(t)
			out, errOut, code := e.runOutput("laptop", append([]string{"run", "--runner", "laptop", "--"}, helper(t, tc.args...)...)...)
			if code != tc.code || !strings.Contains(errOut, "run R1 claimed slice I1.1") {
				t.Fatalf("run exited %d, stdout %q, stderr %q", code, out, errOut)
			}
			slice := e.board().Slices[0]
			if tc.branch != "" {
				assertRunClosed(e, flow.CloseSubmitted, tc.branch)
				if slice.Status != flow.SliceSubmitted || slice.Branch != tc.branch || out != "--flag --\n" || !strings.Contains(errOut, "submitted R1 on "+tc.branch) {
					t.Fatalf("slice %+v, stdout %q, stderr %q", slice, out, errOut)
				}
			} else {
				assertRunClosed(e, flow.CloseReleased, "")
				if slice.Status != flow.SliceStalled || !slice.HoldsExecute || !slice.HoldsReview || out != "" || !strings.Contains(errOut, "dropped R1") {
					t.Fatalf("slice %+v, stdout %q, stderr %q", slice, out, errOut)
				}
			}
			if tc.name == "multiline branch" && !strings.Contains(errOut, "multiple lines") {
				t.Fatal(errOut)
			}
		})
	}
}

func TestRunEnvironment(t *testing.T) {
	e := newBriefEnv(t)
	t.Setenv("METRONOME_GITHUB_APP_PRIVATE_KEY", "private-fixture")
	t.Setenv("METRONOME_RUN_ID", "stale")
	t.Setenv("METRONOME_URL", "http://example.com")
	t.Setenv("METRONOME_ACTOR", "stale")
	path := filepath.Join(e.dir, "environment.json")
	e.ok("laptop", append([]string{"--request-id", "q1", "run", "--slice", "I1.1", "--runner", "laptop", "--"}, helper(t, "env", path, "branch", "metronome/I1.1")...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seen helperEnvironment
	if err := json.Unmarshal(data, &seen); err != nil {
		t.Fatal(err)
	}
	if _, exists := seen.Vars["METRONOME_GITHUB_APP_PRIVATE_KEY"]; exists {
		t.Fatal("runner received the App private key")
	}
	for key, want := range map[string]string{
		"METRONOME_RUN_ID": "R1", "METRONOME_SLICE_ID": "I1.1", "METRONOME_RUNNER": "laptop",
		"METRONOME_URL": "http://metronome.test", "METRONOME_ACTOR": "laptop",
	} {
		if seen.Vars[key] != want {
			t.Errorf("%s = %q, want %q", key, seen.Vars[key], want)
		}
	}
	for _, key := range []string{"METRONOME_BRIEF", "METRONOME_BRANCH_FILE"} {
		path := seen.Vars[key]
		if !filepath.IsAbs(path) || fileExists(path) || fileExists(filepath.Dir(path)) {
			t.Errorf("%s = %q, want an absolute path removed after run", key, path)
		}
	}
	assertRunClosed(e, flow.CloseSubmitted, "metronome/I1.1")
	want := e.ok("laptop", "--request-id", "q1", "next", "--runner", "laptop", "--json")
	if !reflect.DeepEqual(decodeBrief(t, seen.Brief), decodeBrief(t, want)) || seen.Brief != want {
		t.Fatalf("brief = %s, want %s", seen.Brief, want)
	}
}

func TestRunPreservesOutputFiles(t *testing.T) {
	e := newBriefEnv(t)
	output, err := os.Create(filepath.Join(e.dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	args := append([]string{"--server", "http://example.com", "--as", "laptop", "run", "--runner", "laptop", "--"}, helper(t, "file-output", "branch", "metronome/I1.1")...)
	if code := cli.RunWithDeps(t.Context(), args, output, output, e.deps()); code != 0 {
		t.Fatal("runner output file descriptors were not preserved", code)
	}
	assertRunClosed(e, flow.CloseSubmitted, "metronome/I1.1")
}

func TestRunRenewsWhileCommandRuns(t *testing.T) {
	e := newBriefEnv(t)
	pathReady, pathRelease := filepath.Join(e.dir, "ready"), filepath.Join(e.dir, "release")
	cmd := startRun(e, append([]string{"--request-id", "q1", "run", "--runner", "laptop", "--every", "20ms", "--"}, helper(t, "wait", pathReady, pathRelease, "branch", "metronome/I1.1")...)...)
	waitFor(t, func() bool { return fileExists(pathReady) })
	var claimLease time.Time
	waitFor(t, func() bool {
		count := 0
		for _, event := range runEvents(e) {
			switch event.kind {
			case flow.EvSliceClaimed:
				claimLease = event.data.LeaseUntil
			case flow.EvHeartbeat:
				count++
			}
		}
		return count >= 2
	})
	board := e.board()
	if claimLease.IsZero() || len(board.Runs) != 1 || !board.Runs[0].LeaseUntil.After(claimLease) {
		t.Fatalf("lease did not renew: %+v, initial %v", board.Runs, claimLease)
	}
	if err := os.WriteFile(pathRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd.expectExit(t, 0)
	assertRunClosed(e, flow.CloseSubmitted, "metronome/I1.1")
}

func TestRunCommandClosesRun(t *testing.T) {
	for _, tc := range []struct {
		name, every, message, reason, branch string
		action                               []string
	}{
		{"submit", "20ms", "stopped renewing", flow.CloseSubmitted, "metronome/I1.1", []string{"submit", "R1", "--branch", "metronome/I1.1"}},
		{"ask", "5m", "already closed", flow.CloseFlagged, "", []string{"ask", "--run", "R1", "--type", "answer", "--question", "CSV or JSON?"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBriefEnv(t)
			pathReady, pathRelease := filepath.Join(e.dir, "ready"), filepath.Join(e.dir, "release")
			cmd := startRun(e, append([]string{"run", "--runner", "laptop", "--every", tc.every, "--"}, helper(t, "wait", pathReady, pathRelease, "branch", "other/branch", "stdout", "finished")...)...)
			waitFor(t, func() bool { return fileExists(pathReady) })
			e.ok("laptop", tc.action...)
			if tc.name == "submit" {
				waitFor(t, func() bool { return strings.Contains(cmd.errOut.String(), tc.message) })
			}
			if err := os.WriteFile(pathRelease, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd.expectExit(t, 0)
			if cmd.out.String() != "finished\n" || !strings.Contains(cmd.errOut.String(), tc.message) || strings.Contains(cmd.errOut.String(), "retrying in") {
				t.Fatalf("stdout %q, stderr %q", cmd.out.String(), cmd.errOut.String())
			}
			assertRunClosed(e, tc.reason, tc.branch)
		})
	}
}

func TestRunForwardsSignals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		sig      os.Signal
		want     os.Signal
	}{
		{"SIGTERM", false, syscall.SIGTERM, syscall.SIGTERM},
		{"SIGTERM with terminal", true, syscall.SIGTERM, syscall.SIGTERM},
		{"SIGINT", false, os.Interrupt, os.Interrupt},
		{"SIGINT with terminal", true, os.Interrupt, syscall.SIGTERM},
		{"cancellation", false, nil, syscall.SIGTERM},
		{"cancellation with terminal", true, nil, syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBriefEnv(t)
			pathReady, pathSignal := filepath.Join(e.dir, "ready"), filepath.Join(e.dir, "signal")
			cmd := startRunWithTerminal(e, tc.terminal, append([]string{"run", "--runner", "laptop", "--"}, helper(t, "trap", pathReady, pathSignal)...)...)
			waitFor(t, func() bool { return fileExists(pathReady) })
			if tc.sig == nil {
				cmd.cancel()
			} else {
				process, err := os.FindProcess(os.Getpid())
				if err != nil {
					t.Fatal(err)
				}
				if err := process.Signal(tc.sig); err != nil {
					t.Fatal(err)
				}
				if tc.terminal && tc.sig == os.Interrupt {
					lateSignals := make(chan os.Signal, 1)
					signal.Notify(lateSignals, syscall.SIGTERM)
					defer signal.Stop(lateSignals)
					deadline := time.Now().Add(10 * time.Second)
					for !fileExists(pathSignal) && time.Now().Before(deadline) {
						time.Sleep(50 * time.Millisecond)
						if fileExists(pathSignal) {
							break
						}
						if err := process.Signal(syscall.SIGTERM); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			cmd.expectExit(t, 7)
			data, err := os.ReadFile(pathSignal)
			if err != nil || string(data) != tc.want.String() {
				t.Fatalf("signal = %q, err %v, want %s", data, err, tc.want)
			}
			assertRunClosed(e, flow.CloseReleased, "")
		})
	}
}

func TestRunSignalExitStatus(t *testing.T) {
	e := newBriefEnv(t)
	out, code := e.run("laptop", "run", "--runner", "laptop", "--", "sh", "-c", "kill -TERM $$")
	if code != 128+int(syscall.SIGTERM) {
		t.Fatalf("run exited %d: %s", code, out)
	}
	assertRunClosed(e, flow.CloseReleased, "")
}

func TestRunUsageAndClaimErrors(t *testing.T) {
	e := newBriefEnv(t)
	for _, args := range [][]string{
		{"run", "--runner", "laptop"},
		{"run", "--runner", "laptop", "--"},
		{"run", "--", "unused"},
		{"run", "--runner", "laptop", "--every", "0", "--", "unused"},
		{"run", "--runner", "laptop", "--every", "-1s", "--", "unused"},
		{"run", "unexpected", "--runner", "laptop", "--", "unused"},
	} {
		if out, code := e.run("laptop", args...); code != 2 || !strings.Contains(out, "metronome run:") {
			t.Fatalf("%v exited %d: %s", args, code, out)
		}
	}
	if strings.Contains(e.ok("pat", "events"), flow.EvSliceClaimed) {
		t.Fatal("usage error claimed a slice")
	}
	for _, reason := range []flow.ErrorCode{flow.CodeNoWork, flow.CodeNoCapacity} {
		t.Run(string(reason), func(t *testing.T) {
			e := newEnv(t)
			if reason == flow.CodeNoCapacity {
				var specs []flow.SliceSpec
				for index := 0; index <= flow.DefaultConfig().Limits.Execute; index++ {
					specs = append(specs, flow.SliceSpec{Title: fmt.Sprintf("Slice %d", index), Repo: "acme/app"})
				}
				e.approvedItem(flow.BacklogItem{Ref: "ACME-1", Title: "Add export", Owner: "pat"}, specs)
				for index := 0; index < flow.DefaultConfig().Limits.Execute; index++ {
					e.ok("laptop", "next", "--runner", "laptop")
				}
			}
			path := filepath.Join(e.dir, "environment.json")
			out, code := e.run("laptop", append([]string{"run", "--runner", "laptop", "--"}, helper(t, "env", path)...)...)
			if code != 1 || !strings.Contains(out, string(reason)) || fileExists(path) {
				t.Fatalf("run exited %d: %s", code, out)
			}
		})
	}
}

func TestRunDropsIfBriefCannotBeRead(t *testing.T) {
	e := newBriefEnv(t)
	handler := e.http.Transport.(handlerTransport).h
	e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/board" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	path := filepath.Join(e.dir, "environment.json")
	out, code := e.run("laptop", append([]string{"run", "--runner", "laptop", "--"}, helper(t, "env", path)...)...)
	if code != 1 || !strings.Contains(out, "run R1 claimed slice I1.1, but its brief could not be read:") || fileExists(path) {
		t.Fatalf("run exited %d: %s", code, out)
	}
	e.http.Transport = handlerTransport{handler}
	assertRunClosed(e, flow.CloseReleased, "")
}

func TestRunDropsIfCommandCannotStart(t *testing.T) {
	e := newBriefEnv(t)
	out, code := e.run("laptop", "run", "--runner", "laptop", "--", filepath.Join(e.dir, "missing-command"))
	if code != 1 || !strings.Contains(out, "missing-command") {
		t.Fatalf("run exited %d: %s", code, out)
	}
	assertRunClosed(e, flow.CloseReleased, "")
}

func TestRunRetriesRenewalErrors(t *testing.T) {
	e := newBriefEnv(t)
	handler := e.http.Transport.(handlerTransport).h
	var attempts atomic.Int32
	e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") && attempts.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	pathReady, pathRelease := filepath.Join(e.dir, "ready"), filepath.Join(e.dir, "release")
	cmd := startRun(e, append([]string{"run", "--runner", "laptop", "--every", "20ms", "--"}, helper(t, "wait", pathReady, pathRelease)...)...)
	waitFor(t, func() bool { return fileExists(pathReady) })
	waitFor(t, func() bool {
		return strings.Contains(e.ok("pat", "events"), "\t"+flow.EvHeartbeat+"\t")
	})
	if err := os.WriteFile(pathRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd.expectExit(t, 0)
	if !strings.Contains(cmd.errOut.String(), "unavailable") || attempts.Load() < 2 {
		t.Fatal(cmd.errOut.String())
	}
	assertRunClosed(e, flow.CloseReleased, "")
}

func TestRunClosingFailureIsNotClean(t *testing.T) {
	for _, code := range []int{0, 3} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			e := newBriefEnvWithClock(t, newRunClock())
			handler := e.http.Transport.(handlerTransport).h
			e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/claims" && r.Header.Get(api.HeaderRequestID) == "q1" {
					t.Error("global request ID was reused")
				}
				if strings.HasSuffix(r.URL.Path, "/submit") || strings.HasSuffix(r.URL.Path, "/release") {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, "unavailable")
					return
				}
				handler.ServeHTTP(w, r)
			})}
			out, got := e.run("laptop", append([]string{"--request-id", "q1", "run", "--runner", "laptop", "--"}, helper(t, "branch", "metronome/I1.1", "exit", strconv.Itoa(code))...)...)
			want := max(1, code)
			if got != want || !strings.Contains(out, "unavailable") || len(e.board().Runs) != 1 {
				t.Fatalf("run exited %d, want %d: %s", got, want, out)
			}
		})
	}
}

type runClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func newRunClock() *runClock {
	return &runClock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
}

func (clock *runClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *runClock) advance(delay time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(delay)
}

func (clock *runClock) Wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.waits = append(clock.waits, delay)
	clock.now = clock.now.Add(delay)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (transport roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func isCloseRequest(req *http.Request) bool {
	return req.URL.Path == "/v1/runs/R1/submit" || req.URL.Path == "/v1/runs/R1/release"
}

func TestRunCloseRecovers(t *testing.T) {
	for _, tc := range []struct {
		name, branch   string
		code           int
		transportError bool
	}{
		{"submit", "metronome/I1.1", 0, false},
		{"drop after success", "", 0, true},
		{"drop after failure", "", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newRunClock()
			e := newBriefEnvWithClock(t, clock)
			original := e.http.Transport
			attempts := 0
			e.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if isCloseRequest(req) {
					attempts++
					if attempts <= 3 {
						if tc.transportError {
							return nil, errors.New("connection refused")
						}
						return handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if attempts == 2 {
								http.Error(w, `{"code":"internal","message":"unavailable"}`, http.StatusInternalServerError)
							} else {
								http.Error(w, "unavailable", http.StatusServiceUnavailable)
							}
						})}.RoundTrip(req)
					}
				}
				return original.RoundTrip(req)
			})
			out, code := e.run("laptop", append([]string{"run", "--runner", "laptop", "--"}, helper(t, "branch", tc.branch, "exit", strconv.Itoa(tc.code))...)...)
			if code != tc.code || attempts != 4 || strings.Count(out, "retrying in") != 3 || !reflect.DeepEqual(clock.waits, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
				t.Fatalf("exit %d, attempts %d, waits %v: %s", code, attempts, clock.waits, out)
			}
			if tc.branch != "" {
				assertRunClosed(e, flow.CloseSubmitted, tc.branch)
				if !strings.Contains(out, "submitted R1 on "+tc.branch) {
					t.Fatal(out)
				}
			} else {
				assertRunClosed(e, flow.CloseReleased, "")
				if !strings.Contains(out, "dropped R1") {
					t.Fatal(out)
				}
			}
		})
	}
}

func TestRunCloseRecoversAfterExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, branch          string
		commandCode, wantCode int
	}{
		{"submit", "metronome/I1.1", 0, 1},
		{"drop after success", "", 0, 0},
		{"drop after failure", "", 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newRunClock()
			deadline := clock.Now().Add(flow.DefaultConfig().Lease)
			e := newBriefEnvWithClock(t, clock)
			handler := e.http.Transport.(handlerTransport).h
			attempts, reads := 0, 0
			e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isCloseRequest(r) {
					attempts++
					if clock.Now().Before(deadline) {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
				}
				if r.URL.Path == "/v1/runs/R1" && r.Method == http.MethodGet {
					reads++
				}
				handler.ServeHTTP(w, r)
			})}
			out, code := e.run("laptop", append([]string{"run", "--runner", "laptop", "--"}, helper(t, "branch", tc.branch, "exit", strconv.Itoa(tc.commandCode))...)...)
			if code != tc.wantCode || reads != 1 || attempts != len(clock.waits)+1 || !strings.Contains(out, "lease on run R1 expired at "+deadline.Format(time.RFC3339)) {
				t.Fatalf("exit %d, attempts %d, reads %d: %s", code, attempts, reads, out)
			}
			if tc.branch != "" && (!strings.Contains(out, "branch "+tc.branch+" was not submitted") || !strings.Contains(out, "claim slice I1.1 again")) {
				t.Fatal(out)
			}
			assertRunClosed(e, flow.CloseLeaseExpired, "")
			slice := e.board().Slices[0]
			if slice.Status != flow.SliceStalled || !slice.HoldsExecute || !slice.HoldsReview {
				t.Fatalf("unexpected slice: %+v", slice)
			}
		})
	}
}

func TestRunCloseNeverRecovers(t *testing.T) {
	clock := newRunClock()
	start := clock.Now()
	e := newBriefEnvWithClock(t, clock)
	original := e.http.Transport.(handlerTransport)
	e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCloseRequest(r) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		original.h.ServeHTTP(w, r)
	})}
	out, code := e.run("laptop", append([]string{"run", "--runner", "laptop", "--"}, helper(t, "branch", "metronome/I1.1")...)...)
	bound := flow.DefaultConfig().Lease + time.Minute
	if code != 1 || clock.Now().Sub(start) != bound || !strings.Contains(out, "lease on run R1 expired") || !strings.Contains(out, "branch metronome/I1.1 was not submitted") {
		t.Fatalf("exit %d, elapsed %s: %s", code, clock.Now().Sub(start), out)
	}
	remaining, delay := bound, time.Second
	for _, waited := range clock.waits {
		if want := min(remaining, delay); waited != want {
			t.Fatalf("waited %s, want %s", waited, want)
		}
		remaining -= waited
		delay = min(2*delay, 30*time.Second)
	}
	if remaining != 0 {
		t.Fatalf("remaining time %s", remaining)
	}
	e.http.Transport = original
	e.ok("pat", "tick")
	assertRunClosed(e, flow.CloseLeaseExpired, "")
}

func TestRunCloseLostResponse(t *testing.T) {
	clock := newRunClock()
	e := newBriefEnvWithClock(t, clock)
	handler := e.http.Transport.(handlerTransport).h
	var ids []string
	e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCloseRequest(r) {
			ids = append(ids, r.Header.Get(api.HeaderRequestID))
			if len(ids) == 1 {
				response, err := (handlerTransport{handler}).RoundTrip(r)
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("first close failed: %v, %v", response, err)
				}
				response.Body.Close()
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
		}
		handler.ServeHTTP(w, r)
	})}
	out, code := e.run("laptop", append([]string{"--request-id", "q1", "run", "--runner", "laptop", "--"}, helper(t, "branch", "metronome/I1.1")...)...)
	if code != 0 || !reflect.DeepEqual(ids, []string{"metronome-run-R1-close", "metronome-run-R1-close"}) || !strings.Contains(out, "submitted R1 on metronome/I1.1") {
		t.Fatalf("exit %d, IDs %v: %s", code, ids, out)
	}
	assertRunClosed(e, flow.CloseSubmitted, "metronome/I1.1")
}

func TestRunCloseDoesNotRetryInvalid(t *testing.T) {
	clock := newRunClock()
	e := newBriefEnvWithClock(t, clock)
	handler := e.http.Transport.(handlerTransport).h
	attempts := 0
	e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCloseRequest(r) {
			attempts++
			http.Error(w, `{"code":"invalid","message":"invalid branch"}`, http.StatusBadRequest)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	out, code := e.run("laptop", append([]string{"run", "--runner", "laptop", "--"}, helper(t, "branch", "metronome/I1.1")...)...)
	if code != 1 || attempts != 1 || len(clock.waits) != 0 || !strings.Contains(out, "invalid branch") {
		t.Fatalf("exit %d, attempts %d, waits %v: %s", code, attempts, clock.waits, out)
	}
}

func TestRunSignalStopsCloseRetry(t *testing.T) {
	clock := newRunClock()
	e := newBriefEnvWithClock(t, clock)
	handler := e.http.Transport.(handlerTransport).h
	e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCloseRequest(r) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	deps := e.deps()
	deps.Wait = func(ctx context.Context, delay time.Duration) error {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			t.Fatal("signal did not cancel close")
			return errors.New("signal timeout")
		}
	}
	var out bytes.Buffer
	args := append([]string{"--server", "http://metronome.test", "--as", "laptop", "run", "--runner", "laptop", "--"}, helper(t, "branch", "metronome/I1.1")...)
	code := cli.RunWithDeps(context.Background(), args, &out, &out, deps)
	if code != 1 || !strings.Contains(out.String(), "gave up submitting run R1 on branch metronome/I1.1") || !strings.Contains(out.String(), "(signal)") {
		t.Fatalf("exit %d: %s", code, &out)
	}
}

func TestRunCloseUsesRenewedLease(t *testing.T) {
	clock := newRunClock()
	start := clock.Now()
	e := newBriefEnvWithClock(t, clock)
	handler := e.http.Transport.(handlerTransport).h
	recoverAt := start.Add(flow.DefaultConfig().Lease + 2*time.Minute)
	e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCloseRequest(r) && clock.Now().Before(recoverAt) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	pathReady, pathRelease := filepath.Join(e.dir, "ready"), filepath.Join(e.dir, "release")
	cmd := startRun(e, append([]string{"run", "--runner", "laptop", "--every", "20ms", "--"}, helper(t, "wait", pathReady, pathRelease, "branch", "metronome/I1.1")...)...)
	waitFor(t, func() bool { return fileExists(pathReady) })
	clock.advance(5 * time.Minute)
	waitFor(t, func() bool {
		for _, event := range runEvents(e) {
			if event.kind == flow.EvHeartbeat && event.data.LeaseUntil.After(recoverAt) {
				return true
			}
		}
		return false
	})
	if err := os.WriteFile(pathRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd.expectExit(t, 0)
	if clock.Now().Before(recoverAt) {
		t.Fatal("service recovered earlier than expected")
	}
	assertRunClosed(e, flow.CloseSubmitted, "metronome/I1.1")
}
