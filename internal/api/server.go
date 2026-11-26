package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/jystringfellow/metronome/internal/flow"
)

const (
	HeaderActor     = "X-Metronome-Actor"
	HeaderRequestID = "Idempotency-Key"
)

type Board struct {
	Limits flow.Limits  `json:"limits"`
	Usage  flow.Usage   `json:"usage"`
	Items  []flow.Item  `json:"items"`
	Slices []flow.Slice `json:"slices"`
	Runs   []flow.Run   `json:"runs"`
	Flags  []flow.Flag  `json:"flags"`
}

type BacklogRequest struct {
	Items []flow.BacklogItem `json:"items"`
}

type PlanStartRequest struct {
	ItemID string `json:"item_id"`
}

type ProposeRequest struct {
	Slices   []flow.SliceSpec `json:"slices"`
	Approver string           `json:"approver"`
}

type ApproveRequest struct {
	Tier *int `json:"tier"`
}

type FlagRequest struct {
	Subject  flow.Subject `json:"subject"`
	Ask      flow.AskType `json:"ask"`
	Question string       `json:"question"`
	Owner    string       `json:"owner"`
}

type AnswerRequest struct {
	Answer string `json:"answer"`
}

type ClaimRequest struct {
	RunnerID string `json:"runner_id"`
	SliceID  string `json:"slice_id"`
}

type ReportRequest struct {
	Note string `json:"note"`
}

type SubmitRequest struct {
	Branch string `json:"branch"`
}

type GitHubRequest struct {
	SliceID string `json:"slice_id"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	HeadSHA string `json:"head_sha"`
	Success bool   `json:"success"`
	By      string `json:"by"`
}

type server struct {
	eng *flow.Engine
}

func Handler(eng *flow.Engine) http.Handler {
	s := &server{eng: eng}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/board", s.board)
	mux.HandleFunc("GET /v1/inbox/{owner}", s.inbox)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("POST /v1/tick", s.tick)
	mux.HandleFunc("POST /v1/backlog/sync", command(func(r *http.Request, c call, req BacklogRequest) (flow.Result, error) {
		return eng.SyncBacklog(r.Context(), c.actor, c.requestID, req.Items)
	}))
	mux.HandleFunc("POST /v1/plan/start", command(func(r *http.Request, c call, req PlanStartRequest) (flow.Result, error) {
		return eng.StartPlan(r.Context(), c.actor, c.requestID, req.ItemID)
	}))
	mux.HandleFunc("POST /v1/items/{id}/plan/propose", command(func(r *http.Request, c call, req ProposeRequest) (flow.Result, error) {
		return eng.ProposePlan(r.Context(), c.actor, c.requestID, r.PathValue("id"), req.Slices, req.Approver)
	}))
	mux.HandleFunc("POST /v1/items/{id}/plan/approve", command(func(r *http.Request, c call, req ApproveRequest) (flow.Result, error) {
		return eng.ApprovePlan(r.Context(), c.actor, c.requestID, r.PathValue("id"), req.Tier)
	}))
	mux.HandleFunc("POST /v1/items/{id}/plan/drop", command(func(r *http.Request, c call, _ struct{}) (flow.Result, error) {
		return eng.DropPlan(r.Context(), c.actor, c.requestID, r.PathValue("id"))
	}))
	mux.HandleFunc("POST /v1/items/{id}/release-done", command(func(r *http.Request, c call, _ struct{}) (flow.Result, error) {
		return eng.ReleaseDone(r.Context(), c.actor, c.requestID, r.PathValue("id"))
	}))
	mux.HandleFunc("POST /v1/flags", command(func(r *http.Request, c call, req FlagRequest) (flow.Result, error) {
		return eng.RaiseFlag(r.Context(), c.actor, c.requestID, req.Subject, req.Ask, req.Question, req.Owner)
	}))
	mux.HandleFunc("POST /v1/flags/{id}/resolve", command(func(r *http.Request, c call, req AnswerRequest) (flow.Result, error) {
		return eng.ResolveFlag(r.Context(), c.actor, c.requestID, r.PathValue("id"), req.Answer)
	}))
	mux.HandleFunc("POST /v1/claims", command(func(r *http.Request, c call, req ClaimRequest) (flow.Result, error) {
		return eng.Claim(r.Context(), c.actor, c.requestID, req.RunnerID, req.SliceID)
	}))
	mux.HandleFunc("POST /v1/runs/{id}/heartbeat", command(func(r *http.Request, c call, req ReportRequest) (flow.Result, error) {
		return eng.Heartbeat(r.Context(), c.actor, c.requestID, r.PathValue("id"), req.Note)
	}))
	mux.HandleFunc("POST /v1/runs/{id}/flag", command(func(r *http.Request, c call, req FlagRequest) (flow.Result, error) {
		return eng.FlagRun(r.Context(), c.actor, c.requestID, r.PathValue("id"), req.Ask, req.Question, req.Owner)
	}))
	mux.HandleFunc("POST /v1/runs/{id}/submit", command(func(r *http.Request, c call, req SubmitRequest) (flow.Result, error) {
		return eng.Submit(r.Context(), c.actor, c.requestID, r.PathValue("id"), req.Branch)
	}))
	mux.HandleFunc("POST /v1/runs/{id}/release", command(func(r *http.Request, c call, _ struct{}) (flow.Result, error) {
		return eng.ReleaseRun(r.Context(), c.actor, c.requestID, r.PathValue("id"))
	}))
	mux.HandleFunc("POST /v1/github/{event}", command(func(r *http.Request, c call, req GitHubRequest) (flow.Result, error) {
		ctx := r.Context()
		switch r.PathValue("event") {
		case "pr-opened":
			return eng.PROpened(ctx, c.actor, c.requestID, req.SliceID, req.Repo, req.Number, req.HeadSHA)
		case "checks":
			return eng.ChecksCompleted(ctx, c.actor, c.requestID, req.SliceID, req.HeadSHA, req.Success)
		case "ready-for-review":
			return eng.ReadyForReview(ctx, c.actor, c.requestID, req.SliceID)
		case "approved":
			return eng.PRApproved(ctx, c.actor, c.requestID, req.SliceID, req.HeadSHA, req.By)
		case "changes-requested":
			return eng.PRChangesRequested(ctx, c.actor, c.requestID, req.SliceID, req.By)
		case "merged":
			return eng.PRMerged(ctx, c.actor, c.requestID, req.SliceID)
		}
		return flow.Result{}, &flow.Error{Code: flow.CodeNotFound, Message: "unknown GitHub event " + r.PathValue("event")}
	}))
	return mux
}

type call struct {
	actor     string
	requestID string
}

func command[T any](fn func(r *http.Request, c call, req T) (flow.Result, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req T
		if r.ContentLength != 0 {
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				writeError(w, &flow.Error{Code: flow.CodeInvalid, Message: "request body: " + err.Error()})
				return
			}
		}
		c := call{actor: r.Header.Get(HeaderActor), requestID: r.Header.Get(HeaderRequestID)}
		if c.actor == "" {
			writeError(w, &flow.Error{Code: flow.CodeInvalid, Message: "missing " + HeaderActor + " header"})
			return
		}
		res, err := fn(r, c, req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

func (s *server) board(w http.ResponseWriter, _ *http.Request) {
	b := Board{Limits: s.eng.Config().Limits, Items: []flow.Item{}, Slices: []flow.Slice{}, Runs: []flow.Run{}, Flags: []flow.Flag{}}
	s.eng.View(func(st *flow.State) {
		b.Usage = st.Usage()
		for _, it := range st.Items {
			b.Items = append(b.Items, *it)
		}
		for _, sl := range st.Slices {
			b.Slices = append(b.Slices, *sl)
		}
		for _, r := range st.Runs {
			if !r.Closed {
				b.Runs = append(b.Runs, *r)
			}
		}
		for _, f := range st.Flags {
			if !f.Closed {
				b.Flags = append(b.Flags, *f)
			}
		}
	})
	sort.Slice(b.Items, func(i, j int) bool { return idLess(b.Items[i].ID, b.Items[j].ID) })
	sort.Slice(b.Slices, func(i, j int) bool { return idLess(b.Slices[i].ID, b.Slices[j].ID) })
	sort.Slice(b.Runs, func(i, j int) bool { return idLess(b.Runs[i].ID, b.Runs[j].ID) })
	sort.Slice(b.Flags, func(i, j int) bool { return idLess(b.Flags[i].ID, b.Flags[j].ID) })
	writeJSON(w, http.StatusOK, b)
}

func (s *server) inbox(w http.ResponseWriter, r *http.Request) {
	out := []flow.Flag{}
	s.eng.View(func(st *flow.State) {
		for _, f := range st.Inbox(r.PathValue("owner")) {
			out = append(out, *f)
		}
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out := s.eng.EventsAfter(after, limit)
	if out == nil {
		out = []flow.Event{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) tick(w http.ResponseWriter, r *http.Request) {
	res, err := s.eng.Tick(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	var fe *flow.Error
	if !errors.As(err, &fe) {
		writeJSON(w, http.StatusInternalServerError, flow.Error{Code: "internal", Message: err.Error()})
		return
	}
	status := map[flow.ErrorCode]int{
		flow.CodeInvalid:    http.StatusBadRequest,
		flow.CodeNotFound:   http.StatusNotFound,
		flow.CodeConflict:   http.StatusConflict,
		flow.CodeNoCapacity: http.StatusConflict,
		flow.CodeNoWork:     http.StatusConflict,
	}[fe.Code]
	if status == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, fe)
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
