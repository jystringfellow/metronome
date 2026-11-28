package instance

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/github"
)

type Config struct {
	Version     int          `json:"version"`
	Actor       string       `json:"actor"`
	Workstreams []Workstream `json:"workstreams"`
	GitHubPoll  *Poll        `json:"github_poll"`
}

type Workstream struct {
	Name       string             `json:"name"`
	Limits     flow.PartialLimits `json:"limits"`
	AdoptLabel *string            `json:"adopt_label"`
	Intake     *Command           `json:"intake"`
	Publish    []Publisher        `json:"publish"`
}

type Schedule struct {
	Every string `json:"every"`
	Actor string `json:"actor"`
}

func (s Schedule) Interval() time.Duration {
	duration, _ := time.ParseDuration(s.Every)
	return duration
}

type Command struct {
	Schedule
	Command []string `json:"command"`
}

type Publisher struct {
	Command
	Name  string `json:"name"`
	Batch *int   `json:"batch"`
}

func (p Publisher) BatchSize() int {
	if p.Batch == nil {
		return 100
	}
	return *p.Batch
}

type Poll struct {
	Schedule
	AdoptRepos []string `json:"adopt_repos"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	return Parse(file)
}

func Parse(reader io.Reader) (Config, error) {
	var cfg Config
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, fmt.Errorf("expected exactly one JSON value")
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) validate() error {
	if cfg.Version != 1 {
		return fmt.Errorf("version must be 1")
	}
	names := map[string]bool{}
	var labels []string
	for _, ws := range cfg.Workstreams {
		if !flow.ValidWorkstreamName(ws.Name) || names[ws.Name] {
			return fmt.Errorf("invalid or duplicate workstream name %q", ws.Name)
		}
		names[ws.Name] = true
		for name, value := range map[string]*int{"ready": ws.Limits.Ready, "plan": ws.Limits.Plan, "execute": ws.Limits.Execute, "review": ws.Limits.Review, "release": ws.Limits.Release} {
			if value != nil && *value < 0 {
				return fmt.Errorf("workstream %s: limits.%s must be nonnegative", ws.Name, name)
			}
		}
		if ws.AdoptLabel != nil {
			label := strings.TrimSpace(*ws.AdoptLabel)
			if utf8.RuneCountInString(label) > 50 || strings.Contains(label, ",") {
				return fmt.Errorf("workstream %s: invalid adopt_label", ws.Name)
			}
			if label != "" {
				for _, existing := range labels {
					if strings.EqualFold(label, existing) {
						return fmt.Errorf("workstream %s: duplicate adopt_label", ws.Name)
					}
				}
				labels = append(labels, label)
			}
		}
		if ws.Intake != nil {
			if err := ws.Intake.validate(); err != nil {
				return fmt.Errorf("workstream %s intake: %w", ws.Name, err)
			}
		}
		publishers := map[string]bool{}
		for _, pub := range ws.Publish {
			if !flow.ValidWorkstreamName(pub.Name) || publishers[pub.Name] {
				return fmt.Errorf("workstream %s: invalid or duplicate publisher name %q", ws.Name, pub.Name)
			}
			publishers[pub.Name] = true
			if pub.BatchSize() < 1 || pub.BatchSize() > 1000 {
				return fmt.Errorf("publish %s/%s: batch must be 1 to 1000", ws.Name, pub.Name)
			}
			if err := pub.Command.validate(); err != nil {
				return fmt.Errorf("publish %s/%s: %w", ws.Name, pub.Name, err)
			}
		}
	}
	if cfg.GitHubPoll != nil {
		if err := cfg.GitHubPoll.Schedule.validate(); err != nil {
			return fmt.Errorf("github_poll: %w", err)
		}
		for _, repo := range cfg.GitHubPoll.AdoptRepos {
			if !github.ValidRepo(repo) {
				return fmt.Errorf("github_poll: adopt_repos must be OWNER/NAME")
			}
		}
	}
	return nil
}

func (s Schedule) validate() error {
	if s.Interval() <= 0 {
		return fmt.Errorf("every must be a positive duration")
	}
	return nil
}

func (c Command) validate() error {
	if err := c.Schedule.validate(); err != nil {
		return err
	}
	if len(c.Command) == 0 || c.Command[0] == "" {
		return fmt.Errorf("command must have a nonempty executable")
	}
	return nil
}

func (cfg *Config) ResolveActors(fallback string) error {
	if cfg.Actor == "" {
		cfg.Actor = fallback
	}
	if cfg.Actor == "" {
		return fmt.Errorf("actor must not be empty")
	}
	resolve := func(s *Schedule) {
		if s.Actor == "" {
			s.Actor = cfg.Actor
		}
	}
	for idx := range cfg.Workstreams {
		ws := &cfg.Workstreams[idx]
		if ws.Intake != nil {
			resolve(&ws.Intake.Schedule)
		}
		for idx := range ws.Publish {
			resolve(&ws.Publish[idx].Schedule)
		}
	}
	if cfg.GitHubPoll != nil {
		resolve(&cfg.GitHubPoll.Schedule)
	}
	return nil
}
