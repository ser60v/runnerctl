package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"
)

type runnerView struct {
	ID                string  `json:"id"`
	Status            string  `json:"status"`
	Phase             *string `json:"phase"`
	SiteID            *string `json:"site_id"`
	DurationSeconds   float64 `json:"duration_seconds"`
	ObservationSource string  `json:"observation_source"`
	ManagerStatus     string  `json:"manager_status"`
	AssignedRuns      []struct {
		ID string `json:"id"`
	} `json:"assigned_runs"`
}

func runnerStatus(status string) bool {
	return terminal(status) || status == "queued" || status == "reserved" || status == "starting" || status == "running" || status == "cancelling"
}

func optionalText(value *string) string {
	if value == nil || *value == "" {
		return "—"
	}
	return safeText(*value, 64)
}

func (a *App) printRunners(raw json.RawMessage) error {
	var envelope struct {
		Items []runnerView `json:"items"`
		Total int          `json:"total"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tPHASE\tSITE\tJOBS\tDURATION\tSOURCE")
	for _, r := range envelope.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", safeText(r.ID, 64), safeText(r.Status, 40), optionalText(r.Phase), optionalText(r.SiteID), len(r.AssignedRuns), duration(r.DurationSeconds), safeText(r.ObservationSource, 40))
	}
	fmt.Fprintf(w, "Total: %d. List uses API records; runners get checks manager state.\n", envelope.Total)
	return w.Flush()
}

func (a *App) watchOptions(name string, args []string) (time.Duration, time.Duration, error) {
	f := a.flags(name)
	interval := f.Duration("interval", 5*time.Second, "poll interval")
	timeout := f.Duration("timeout", 15*time.Minute, "maximum duration")
	if err := f.Parse(args); err != nil {
		return 0, 0, usage(err.Error())
	}
	if len(f.Args()) > 0 || *interval < time.Second || *interval > time.Minute || *timeout <= 0 || *timeout > 24*time.Hour {
		return 0, 0, usage("interval must be 1s–1m and timeout must be greater than zero and at most 24h")
	}
	return *interval, *timeout, nil
}

func (a *App) runnerWatchCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage("watch requires a runner UUID")
	}
	id := args[0]
	if err := validateUUID("runner", id); err != nil {
		return usage(err.Error())
	}
	interval, timeout, err := a.watchOptions("runners watch", args[1:])
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return a.observe(ctx, c, "/api/app/v1/runners/"+id, id, interval)
}

func (a *App) statusCommand(ctx context.Context, args []string) error {
	watch := len(args) > 0 && args[0] == "watch"
	var interval, timeout time.Duration
	if watch {
		var err error
		interval, timeout, err = a.watchOptions("status watch", args[1:])
		if err != nil {
			return err
		}
	} else if len(args) != 0 {
		return usage("status accepts no arguments or watch [--interval 5s] [--timeout 15m]")
	}
	server := a.server
	if server == "" {
		if c, err := loadCredential(a.context); err == nil {
			server = c.Server
		} else if !os.IsNotExist(err) {
			return err
		} else {
			server = defaultServer
		}
	}
	// Public health never receives a PAT, even when the caller is authenticated.
	c := newClient(server, "")
	if !watch {
		res, err := c.json(ctx, "GET", "/api/app/v1/status", nil)
		if err != nil {
			return err
		}
		return a.output(res.Body)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return a.observe(ctx, c, "/api/app/v1/status", "", interval)
}

func (a *App) retryNotice(delay time.Duration) {
	if a.json {
		_ = json.NewEncoder(a.Err).Encode(map[string]any{"warning": map[string]any{"code": "temporarily_unavailable", "retry_after_seconds": delay.Seconds()}})
	} else {
		fmt.Fprintln(a.Err, "runnerctl: temporarily unavailable; retrying")
	}
}

// Observations remain independent from control actions. No read timeout or job
// outcome can cause a cancellation, restart, or change to a customer's pool.
func (a *App) observe(ctx context.Context, c *client, path, runnerID string, interval time.Duration) error {
	for {
		res, err := c.json(ctx, "GET", path, nil)
		delay := interval
		if err != nil {
			var ae *APIError
			if !errors.As(err, &ae) || (ae.Status != 429 && ae.Status != 503) {
				return err
			}
			if ae.RetryAfter > delay {
				delay = ae.RetryAfter
			}
			a.retryNotice(delay)
		} else {
			var r runnerView
			if err := json.Unmarshal(res.Body, &r); err != nil || r.Status == "" {
				return errors.New("invalid observation response")
			}
			if runnerID != "" && r.ID != runnerID {
				return errors.New("runner observation identity mismatch")
			}
			if a.json {
				if err := a.output(res.Body); err != nil {
					return err
				}
			} else if runnerID != "" {
				_, err = fmt.Fprintf(a.Out, "%s  %s  phase=%s  site=%s  manager=%s  %s\n", time.Now().UTC().Format(time.RFC3339), safeText(r.Status, 40), optionalText(r.Phase), optionalText(r.SiteID), safeText(r.ManagerStatus, 40), duration(r.DurationSeconds))
			} else {
				// Keep capacity, freshness and incident detail visible in human mode.
				err = a.output(res.Body)
			}
			if err != nil {
				return err
			}
			if runnerID != "" && r.ObservationSource == "manager" && r.ManagerStatus == "available" && r.Phase != nil && *r.Phase == "done" && terminal(r.Status) {
				if r.Status != "succeeded" {
					return &commandError{message: "runner ended with status " + r.Status, exit: 4}
				}
				return nil
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
