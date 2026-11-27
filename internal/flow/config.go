package flow

import "time"

type Limits struct {
	Ready            int `json:"ready"`
	ReadyRefillBelow int `json:"ready_refill_below"`
	Plan             int `json:"plan"`
	Execute          int `json:"execute"`
	Review           int `json:"review"`
	PerApprover      int `json:"per_approver"`
	Release          int `json:"release"`
}

type Config struct {
	Limits       Limits        `json:"limits"`
	Lease        time.Duration `json:"lease"`
	StallTimeout time.Duration `json:"stall_timeout"`
	AskTimeout   time.Duration `json:"ask_timeout"`
}

type StageLimits struct {
	Ready   int `json:"ready"`
	Plan    int `json:"plan"`
	Execute int `json:"execute"`
	Review  int `json:"review"`
	Release int `json:"release"`
}

func (l Limits) Stages() StageLimits {
	return StageLimits{Ready: l.Ready, Plan: l.Plan, Execute: l.Execute, Review: l.Review, Release: l.Release}
}

type PartialLimits struct {
	Ready   *int `json:"ready,omitempty"`
	Plan    *int `json:"plan,omitempty"`
	Execute *int `json:"execute,omitempty"`
	Review  *int `json:"review,omitempty"`
	Release *int `json:"release,omitempty"`
}

func (p PartialLimits) merge(l StageLimits) (StageLimits, error) {
	for target, value := range map[*int]*int{&l.Ready: p.Ready, &l.Plan: p.Plan, &l.Execute: p.Execute, &l.Review: p.Review, &l.Release: p.Release} {
		if value != nil {
			if *value < 0 {
				return l, fail(CodeInvalid, "limits must be nonnegative")
			}
			*target = *value
		}
	}
	return l, nil
}

func DefaultConfig() Config {
	return Config{
		Limits: Limits{
			Ready:            10,
			ReadyRefillBelow: 6,
			Plan:             3,
			Execute:          5,
			Review:           8,
			PerApprover:      2,
			Release:          2,
		},
		Lease:        15 * time.Minute,
		StallTimeout: 2 * time.Hour,
		AskTimeout:   24 * time.Hour,
	}
}
