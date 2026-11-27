package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/github"
	"github.com/jystringfellow/metronome/internal/store"
)

const defaultServer = "http://127.0.0.1:7340"

const usage = `metronome: a flow record for AI delivery work

Service:
  serve [--addr 127.0.0.1:7340] [--db metronome.db] [--tick 30s]

Looking:
  board [--workstream NAME] [--json]  limits, usage, items, slices, open runs and asks
  inbox OWNER                     open asks for one person, oldest first
  events [--after SEQ]            the event log

Workstreams:
  workstream create NAME [--ready N --plan N --execute N --review N --release N]
  workstream set NAME --LIMIT N    change one or more stage limits
  workstream list [--json]        limits and usage per workstream

Backlog and plan:
  backlog sync [--workstream NAME] FILE  add ranked items (default workstream if omitted)
                         skips items with "claimed_by" set
                         items may carry "slices" (as plan propose) and an optional "predicted_tier" (default 1); see docs/backlog.md
  intake --workstream NAME [--every D] -- CMD [ARGS...]  mirror Ready from a source command; see docs/intake.md
  plan start [--workstream NAME] [ITEM]  pull the top ready item with capacity
                         an item with backlog slices is proposed and its owner asked to approve
  plan propose ITEM --slices FILE [--approver WHO]
                         a slice may list "after": [N] positions that merge first
  plan approve ITEM [--tier N]
  plan drop ITEM

Runners (the connector operations):
  next --runner ID [--workstream NAME] [--slice S]  claim the next slice whose dependencies have merged (next_work)
  report RUN [--note TEXT]        renew the lease (heartbeat)
  ask (--run R | --item I | --slice S) --type ASK --question TEXT [--owner WHO]
  submit RUN --branch BRANCH      the work will arrive on BRANCH
  drop RUN                        give the run up; the slice keeps its slots

People:
  answer FLAG TEXT
  release-done ITEM               finish the release of a tier 2 or 3 item

GitHub:
  github poll [--every 1m]        record what GitHub shows for each submitted slice's branch, once or on a repeat
  github pr-opened --slice S --repo OWNER/NAME --number N --head SHA
  github checks --slice S --head SHA (--success | --failure)
  github ready-for-review --slice S
  github approved --slice S --head SHA --by WHO
  github changes-requested --slice S --by WHO
  github merged --slice S [--by WHO]   WHO merged it; before an approval, WHO approves it

Maintenance:
  tick                            expire leases and asks now

Global flags (before the command): --server URL (or METRONOME_URL), --as WHO (or METRONOME_ACTOR),
--request-id ID to make a command safe to repeat.
`

type app struct {
	out, errOut io.Writer
	client      *api.Client
	requestID   string
	prs         github.Source
}

type Deps struct {
	HTTP   *http.Client
	GitHub github.Source
}

func Run(ctx context.Context, args []string, out, errOut io.Writer) int {
	return RunWithDeps(ctx, args, out, errOut, Deps{})
}

func RunWithHTTP(ctx context.Context, args []string, out, errOut io.Writer, hc *http.Client) int {
	return RunWithDeps(ctx, args, out, errOut, Deps{HTTP: hc})
}

func RunWithDeps(ctx context.Context, args []string, out, errOut io.Writer, deps Deps) int {
	if deps.HTTP == nil {
		deps.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if deps.GitHub == nil {
		deps.GitHub = github.GH{}
	}
	global := flag.NewFlagSet("metronome", flag.ContinueOnError)
	global.SetOutput(errOut)
	global.Usage = func() { fmt.Fprint(errOut, usage) }
	server := global.String("server", envOr("METRONOME_URL", defaultServer), "")
	actor := global.String("as", envOr("METRONOME_ACTOR", os.Getenv("USER")), "")
	requestID := global.String("request-id", "", "")
	if err := global.Parse(args); err != nil {
		return 2
	}
	rest := global.Args()
	if len(rest) == 0 {
		global.Usage()
		return 2
	}
	a := &app{out: out, errOut: errOut, requestID: *requestID, prs: deps.GitHub,
		client: &api.Client{BaseURL: *server, Actor: *actor, HTTP: deps.HTTP}}
	if err := a.dispatch(ctx, rest[0], rest[1:]); err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(errOut, "metronome %s: %s\n", rest[0], ue.msg)
			return 2
		}
		fmt.Fprintf(errOut, "metronome %s: %v\n", rest[0], err)
		return 1
	}
	return 0
}

type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func usagef(format string, args ...any) error { return usageError{msg: fmt.Sprintf(format, args...)} }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (a *app) dispatch(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "serve":
		return a.serve(ctx, args)
	case "board":
		return a.board(ctx, args)
	case "inbox":
		return a.inbox(ctx, args)
	case "events":
		return a.events(ctx, args)
	case "backlog":
		return a.backlog(ctx, args)
	case "intake":
		return a.intake(ctx, args)
	case "workstream":
		return a.workstream(ctx, args)
	case "plan":
		return a.plan(ctx, args)
	case "next":
		return a.next(ctx, args)
	case "report":
		return a.report(ctx, args)
	case "ask":
		return a.ask(ctx, args)
	case "submit":
		return a.submit(ctx, args)
	case "drop":
		return a.runOnly(ctx, args, "drop", "release")
	case "answer":
		return a.answer(ctx, args)
	case "release-done":
		return a.itemOnly(ctx, args, "release-done", "/release-done")
	case "github":
		return a.github(ctx, args)
	case "tick":
		return a.post(ctx, "/v1/tick", nil)
	case "help", "-h", "--help":
		fmt.Fprint(a.out, usage)
		return nil
	}
	return usagef("unknown command; run metronome help")
}

func (a *app) serve(ctx context.Context, args []string) error {
	fs := newFlags("serve")
	addr := fs.String("addr", "127.0.0.1:7340", "")
	dbPath := fs.String("db", "metronome.db", "")
	tick := fs.Duration("tick", 30*time.Second, "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.OpenSQLite(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), st, nil)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: api.Handler(eng), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		t := time.NewTicker(*tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := eng.Tick(ctx); err != nil {
					fmt.Fprintf(a.errOut, "tick: %v\n", err)
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(a.out, "metronome serving on http://%s with %s\n", ln.Addr(), *dbPath)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (a *app) board(ctx context.Context, args []string) error {
	fs := newFlags("board")
	ws := fs.String("workstream", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	var b api.Board
	if err := a.client.Get(ctx, "/v1/board?workstream="+url.QueryEscape(*ws), &b); err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(a.out, b)
	}
	l, u := b.Limits, b.Usage
	for who, n := range u.PerApprover {
		if n > l.PerApprover {
			fmt.Fprintf(a.out, "approver %s has %d PRs in review, over the cap of %d\n", who, n, l.PerApprover)
		}
	}
	for _, ws := range b.Workstreams {
		l, u := ws.Limits, ws.Usage
		fmt.Fprintf(a.out, "workstream %s  ready %d/%d  plan %d/%d  execute %d/%d  review %d/%d  release %d/%d\n", ws.Name, u.Ready, l.Ready, u.Plan, l.Plan, u.Execute, l.Execute, u.Review, l.Review, u.Release, l.Release)
		tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "\nITEM\tSTATUS\tTIER\tOWNER\tTITLE")
		for _, it := range b.Items {
			if it.Workstream != ws.Name {
				continue
			}
			if it.Status == flow.ItemDone || it.Status == flow.ItemDropped || it.Status == flow.ItemShelved {
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", it.ID, it.Status, it.PredictedTier, it.Owner, it.Title)
		}
		fmt.Fprintln(tw, "\nSLICE\tSTATUS\tSLOTS\tRUN\tPR\tTITLE")
		for _, sl := range b.Slices {
			if sl.Workstream != ws.Name {
				continue
			}
			if sl.Status == flow.SliceMerged {
				continue
			}
			pr := "-"
			if sl.PR != nil {
				pr = fmt.Sprintf("%s#%d", sl.PR.Repo, sl.PR.Number)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", sl.ID, sliceStatus(sl), slots(sl), dash(sl.RunID), pr, sl.Title)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
	if len(b.Flags) > 0 {
		fmt.Fprintln(tw, "\nASK\tTYPE\tOWNER\tON\tWAITING\tQUESTION")
		for _, f := range b.Flags {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", f.ID, f.Ask, f.Owner, f.Subject.ID, waiting(f.RaisedAt), f.Question)
		}
	}
	return tw.Flush()
}

func sliceStatus(sl flow.Slice) string {
	if len(sl.WaitsOn) > 0 {
		return string(sl.Status) + " (waits on " + strings.Join(sl.WaitsOn, ", ") + ")"
	}
	if sl.Rework {
		return string(sl.Status) + " (rework)"
	}
	return string(sl.Status)
}

func slots(sl flow.Slice) string {
	var s []string
	if sl.HoldsExecute {
		s = append(s, "execute")
	}
	if sl.HoldsReview {
		s = append(s, "review")
	}
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, "+")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func waiting(since time.Time) string {
	return time.Since(since).Round(time.Minute).String()
}

func (a *app) inbox(ctx context.Context, args []string) error {
	fs := newFlags("inbox")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	var flags []flow.Flag
	if err := a.client.Get(ctx, "/v1/inbox/"+fs.Arg(0), &flags); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ASK\tTYPE\tON\tWAITING\tQUESTION")
	for _, f := range flags {
		fmt.Fprintf(tw, "%s\t%s\t%s %s\t%s\t%s\n", f.ID, f.Ask, f.Subject.Kind, f.Subject.ID, waiting(f.RaisedAt), f.Question)
	}
	return tw.Flush()
}

func (a *app) events(ctx context.Context, args []string) error {
	fs := newFlags("events")
	after := fs.Int64("after", 0, "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	var evs []flow.Event
	if err := a.client.Get(ctx, fmt.Sprintf("/v1/events?after=%d", *after), &evs); err != nil {
		return err
	}
	for _, ev := range evs {
		fmt.Fprintf(a.out, "%d\t%s\t%s\t%s\t%s\n", ev.Seq, ev.At.Format(time.RFC3339), ev.Actor, ev.Type, ev.Data)
	}
	return nil
}

func (a *app) backlog(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "sync" {
		return usagef("usage: backlog sync [--workstream NAME] FILE")
	}
	fs := newFlags("backlog sync")
	ws := fs.String("workstream", "", "")
	if err := parse(fs, args[1:], 1); err != nil {
		return err
	}
	var req api.BacklogRequest
	if err := readJSON(fs.Arg(0), &req); err != nil {
		return err
	}
	if *ws != "" {
		if req.Workstream != "" && req.Workstream != *ws {
			return usagef("workstream flag and file disagree")
		}
		req.Workstream = *ws
	}
	return a.post(ctx, "/v1/backlog/sync", req)
}

func (a *app) plan(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("usage: plan start|propose|approve|drop")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "start":
		fs := newFlags("plan start")
		ws := fs.String("workstream", "", "")
		if err := parse(fs, args, -1); err != nil {
			return err
		}
		return a.post(ctx, "/v1/plan/start", api.PlanStartRequest{ItemID: fs.Arg(0), Workstream: *ws})
	case "propose":
		fs := newFlags("plan propose")
		file := fs.String("slices", "", "")
		approver := fs.String("approver", "", "")
		if err := parse(fs, args, 1); err != nil {
			return err
		}
		if *file == "" {
			return usagef("--slices FILE is required")
		}
		var slices []flow.SliceSpec
		if err := readJSON(*file, &slices); err != nil {
			return err
		}
		return a.post(ctx, "/v1/items/"+fs.Arg(0)+"/plan/propose", api.ProposeRequest{Slices: slices, Approver: *approver})
	case "approve":
		fs := newFlags("plan approve")
		tier := fs.Int("tier", -1, "")
		if err := parse(fs, args, 1); err != nil {
			return err
		}
		req := api.ApproveRequest{}
		if *tier >= 0 {
			req.Tier = tier
		}
		return a.post(ctx, "/v1/items/"+fs.Arg(0)+"/plan/approve", req)
	case "drop":
		return a.itemOnly(ctx, args, "plan drop", "/plan/drop")
	}
	return usagef("usage: plan start|propose|approve|drop")
}

func (a *app) next(ctx context.Context, args []string) error {
	fs := newFlags("next")
	runner := fs.String("runner", "", "")
	slice := fs.String("slice", "", "")
	ws := fs.String("workstream", "", "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	if *runner == "" {
		return usagef("--runner ID is required")
	}
	var res flow.Result
	if err := a.client.Post(ctx, "/v1/claims", a.requestID, api.ClaimRequest{RunnerID: *runner, SliceID: *slice, Workstream: *ws}, &res); err != nil {
		return err
	}
	c, ok := flow.ClaimFrom(res)
	if !ok {
		return fmt.Errorf("the claim returned no run")
	}
	fmt.Fprintf(a.out, "run %s claimed slice %s; lease until %s\n", c.RunID, c.SliceID, c.LeaseUntil.Local().Format(time.Kitchen))
	fmt.Fprintf(a.out, "workstream: %s\n", c.Workstream)
	return nil
}

func (a *app) report(ctx context.Context, args []string) error {
	fs := newFlags("report")
	note := fs.String("note", "", "")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	return a.post(ctx, "/v1/runs/"+fs.Arg(0)+"/heartbeat", api.ReportRequest{Note: *note})
}

func (a *app) ask(ctx context.Context, args []string) error {
	fs := newFlags("ask")
	run := fs.String("run", "", "")
	item := fs.String("item", "", "")
	slice := fs.String("slice", "", "")
	ask := fs.String("type", "", "")
	question := fs.String("question", "", "")
	owner := fs.String("owner", "", "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	req := api.FlagRequest{Ask: flow.AskType(*ask), Question: *question, Owner: *owner}
	switch {
	case *run != "" && *item == "" && *slice == "":
		return a.post(ctx, "/v1/runs/"+*run+"/flag", req)
	case *item != "" && *run == "" && *slice == "":
		req.Subject = flow.Subject{Kind: flow.SubjectItem, ID: *item}
	case *slice != "" && *run == "" && *item == "":
		req.Subject = flow.Subject{Kind: flow.SubjectSlice, ID: *slice}
	default:
		return usagef("give exactly one of --run, --item or --slice")
	}
	return a.post(ctx, "/v1/flags", req)
}

func (a *app) submit(ctx context.Context, args []string) error {
	fs := newFlags("submit")
	branch := fs.String("branch", "", "")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	return a.post(ctx, "/v1/runs/"+fs.Arg(0)+"/submit", api.SubmitRequest{Branch: *branch})
}

func (a *app) answer(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return usagef("usage: answer FLAG TEXT")
	}
	return a.post(ctx, "/v1/flags/"+args[0]+"/resolve", api.AnswerRequest{Answer: args[1]})
}

func (a *app) runOnly(ctx context.Context, args []string, name, action string) error {
	if len(args) != 1 {
		return usagef("usage: %s RUN", name)
	}
	return a.post(ctx, "/v1/runs/"+args[0]+"/"+action, nil)
}

func (a *app) itemOnly(ctx context.Context, args []string, name, suffix string) error {
	if len(args) != 1 {
		return usagef("usage: %s ITEM", name)
	}
	return a.post(ctx, "/v1/items/"+args[0]+suffix, nil)
}

func (a *app) github(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("usage: github EVENT [flags]")
	}
	event, args := args[0], args[1:]
	if event == "poll" {
		return a.githubPoll(ctx, args)
	}
	fs := newFlags("github " + event)
	var req api.GitHubRequest
	fs.StringVar(&req.SliceID, "slice", "", "")
	fs.StringVar(&req.Repo, "repo", "", "")
	fs.IntVar(&req.Number, "number", 0, "")
	fs.StringVar(&req.HeadSHA, "head", "", "")
	fs.StringVar(&req.By, "by", "", "")
	success := fs.Bool("success", false, "")
	failure := fs.Bool("failure", false, "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	if event == "checks" && *success == *failure {
		return usagef("give exactly one of --success or --failure")
	}
	req.Success = *success
	return a.post(ctx, "/v1/github/"+event, req)
}

func (a *app) githubPoll(ctx context.Context, args []string) error {
	fs := newFlags("github poll")
	every := fs.Duration("every", 0, "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	if *every < 0 {
		return usagef("--every must be positive")
	}
	p := github.Poller{Client: a.client, Source: a.prs}
	if *every == 0 {
		return a.pollOnce(ctx, p)
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	t := time.NewTicker(*every)
	defer t.Stop()
	for {
		if err := a.pollOnce(ctx, p); err != nil {
			fmt.Fprintf(a.errOut, "poll: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (a *app) pollOnce(ctx context.Context, p github.Poller) error {
	res, err := p.Once(ctx)
	if err != nil {
		return err
	}
	for _, act := range res.Recorded {
		fmt.Fprintln(a.out, "recorded "+act.String())
	}
	for _, n := range res.Notes {
		fmt.Fprintln(a.out, "note "+n)
	}
	for _, e := range res.Errors {
		fmt.Fprintln(a.errOut, "error "+e)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d slice(s) could not be polled", len(res.Errors))
	}
	return nil
}

func (a *app) post(ctx context.Context, path string, body any) error {
	var res flow.Result
	if err := a.client.Post(ctx, path, a.requestID, body, &res); err != nil {
		return err
	}
	if res.Duplicate {
		fmt.Fprintln(a.out, "already recorded; nothing changed")
	}
	for _, ev := range res.Events {
		fmt.Fprintf(a.out, "%d\t%s\t%s\n", ev.Seq, ev.Type, ev.Data)
	}
	for _, note := range res.Notes {
		fmt.Fprintln(a.out, "note "+note)
	}
	if len(res.Events) == 0 {
		fmt.Fprintln(a.out, "no change")
	}
	return nil
}

func (a *app) workstream(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("usage: workstream create|set|list")
	}
	sub := args[0]
	fs := newFlags("workstream " + sub)
	if sub == "list" {
		asJSON := fs.Bool("json", false, "")
		if err := parse(fs, args[1:], 0); err != nil {
			return err
		}
		var views []flow.WorkstreamView
		if err := a.client.Get(ctx, "/v1/workstreams", &views); err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(a.out, views)
		}
		tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "WORKSTREAM\tREADY\tPLAN\tEXECUTE\tREVIEW\tRELEASE")
		for _, ws := range views {
			l, u := ws.Limits, ws.Usage
			fmt.Fprintf(tw, "%s\t%d/%d\t%d/%d\t%d/%d\t%d/%d\t%d/%d\n", ws.Name, u.Ready, l.Ready, u.Plan, l.Plan, u.Execute, l.Execute, u.Review, l.Review, u.Release, l.Release)
		}
		return tw.Flush()
	}
	if sub != "create" && sub != "set" {
		return usagef("usage: workstream create|set|list")
	}
	var p flow.PartialLimits
	values := map[string]**int{"ready": &p.Ready, "plan": &p.Plan, "execute": &p.Execute, "review": &p.Review, "release": &p.Release}
	flags := map[string]*int{}
	for name := range values {
		flags[name] = fs.Int(name, -1, "")
	}
	if err := parse(fs, args[1:], 1); err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) { *values[f.Name] = flags[f.Name] })
	if sub == "create" {
		return a.post(ctx, "/v1/workstreams", api.WorkstreamCreateRequest{Name: fs.Arg(0), PartialLimits: p})
	}
	if p == (flow.PartialLimits{}) {
		return usagef("give at least one limit")
	}
	return a.post(ctx, "/v1/workstreams/"+url.PathEscape(fs.Arg(0))+"/limits", p)
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string, positional int) error {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return usagef("%v", err)
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if positional == -1 && len(pos) > 1 {
		return usagef("want at most one argument")
	}
	if positional >= 0 && len(pos) != positional {
		return usagef("want %d argument(s), got %d; run metronome help", positional, len(pos))
	}
	return fs.Parse(pos)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
