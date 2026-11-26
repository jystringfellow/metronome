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
