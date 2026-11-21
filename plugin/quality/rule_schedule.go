package quality

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/robfig/cron/v3"
)

const defaultRuleCron = "*/30 * * * *"
const ruleTimersKey = "explicit_rule_timers"

// Individually starting an observation never opens the legacy instance-wide gate.
func (en *Engine) timerAllowed(r *Rule) bool {
	return r.SchedulingEnabled && (en.cfg.SchedulingEnabled || en.timers[r.ID])
}

func (en *Engine) changedTimer(id string, on bool) map[string]bool {
	if en.timers[id] == on {
		return nil
	}
	next := make(map[string]bool, len(en.timers)+1)
	maps.Copy(next, en.timers)
	if on {
		next[id] = true
	} else {
		delete(next, id)
	}
	return next
}

// An explicit instance-wide stop revokes individual authorizations as well.
func (en *Engine) writeScheduleSettings(c Config, revokeTimers bool) error {
	return en.commitLocked(en.scheduleSettingsChanges(c, revokeTimers, en.cfg.SchedulingEnabled))
}

func (en *Engine) scheduleSettingsChanges(c Config, revokeTimers, previouslyEnabled bool) *changeSet {
	update := changes()
	update.settings = &c
	if revokeTimers {
		update.timers = map[string]bool{}
		for _, rule := range en.rules {
			if rule.PendingSchedule != nil {
				next := cloneRule(rule)
				next.PendingSchedule = nil
				update.rules[rule.ID] = next
			}
		}
	}
	if !previouslyEnabled && c.SchedulingEnabled {
		now := en.now()
		for _, rule := range en.rules {
			if ruleScheduled(rule) && !en.timers[rule.ID] && (rule.PendingSchedule != nil || !rule.NextRun.IsZero() && !rule.NextRun.After(now)) {
				next := cloneRule(rule)
				next.PendingSchedule = nil
				next.NextRun = nextRun(*next, now)
				update.rules[rule.ID] = next
			}
		}
	}
	return update
}

var fiveFieldCron = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func sameRuleSchedule(a, b *Rule) bool {
	return a.Cron == b.Cron && a.IntervalSeconds == b.IntervalSeconds && a.Timezone == b.Timezone
}

func prepareRuleSchedule(in, old *Rule) error {
	if in.Cron == "" && in.Timezone == "" && in.IntervalSeconds == 0 {
		if old != nil {
			in.Cron, in.Timezone, in.IntervalSeconds = old.Cron, old.Timezone, old.IntervalSeconds
		} else {
			in.Cron = defaultRuleCron
		}
	}
	// Older clients can echo a stored legacy plan, but cannot create or change one.
	if old != nil && sameRuleSchedule(in, old) {
		return nil
	}
	if in.Timezone != "" || in.IntervalSeconds != 0 {
		return fmt.Errorf("invalid_request: use five-field cron in server time; interval_seconds and timezone are legacy fields")
	}
	in.Cron = strings.Join(strings.Fields(in.Cron), " ")
	return nil
}

func ruleScheduleLocation(r *Rule, server *time.Location) (*time.Location, error) {
	if r.Timezone == "" {
		return server, nil
	}
	return time.LoadLocation(r.Timezone)
}

// ruleNextInstant validates even a manual-only plan. Intervals use elapsed time;
// Cron uses wall time in the selected location, never the UTC ticker's location.
func ruleNextInstant(r *Rule, now time.Time, server *time.Location) (time.Time, error) {
	loc, err := ruleScheduleLocation(r, server)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid_request: unknown legacy schedule timezone")
	}
	if r.Cron == "" {
		if r.IntervalSeconds < 60 || r.IntervalSeconds > 31*86400 {
			return time.Time{}, fmt.Errorf("invalid_cron: five fields required")
		}
		return now.Add(time.Duration(r.IntervalSeconds) * time.Second).UTC(), nil
	}
	if len(strings.Fields(r.Cron)) != 5 || strings.ContainsAny(r.Cron, "=@") {
		return time.Time{}, fmt.Errorf("invalid_cron: five fields required, without timezone or descriptors")
	}
	plan, err := fiveFieldCron.Parse(r.Cron)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid_cron: invalid field, range or step")
	}
	next := plan.Next(now.In(loc))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("invalid_cron: no matching date")
	}
	return next.UTC(), nil
}

func ruleScheduled(r *Rule) bool {
	return r.SchedulingEnabled && ruleStatus(r) == "detecting"
}

func nextRun(r Rule, now time.Time) time.Time {
	if !ruleScheduled(&r) {
		return time.Time{}
	}
	next, _ := ruleNextInstant(&r, now, time.Local)
	return next
}

type ruleView struct {
	Rule
	Handling        []HandlingSummary `json:"handling,omitempty"`
	TimerActive     bool              `json:"timer_active"`
	TimerWaitReason string            `json:"timer_wait_reason,omitempty"`
	NextRunLocal    string            `json:"next_run_local,omitempty"`
}

func viewRule(r Rule) ruleView {
	view := ruleView{Rule: r, TimerActive: r.timerActive, TimerWaitReason: r.timerWaitReason}
	if ruleScheduled(&r) && !r.NextRun.IsZero() {
		if loc, err := ruleScheduleLocation(&r, time.Local); err == nil {
			view.NextRunLocal = r.NextRun.In(loc).Format(time.RFC3339)
		}
	}
	return view
}

type ruleWrite struct {
	Rule
	Activation string
}

func decodeRuleWrite(raw []byte, update bool) (ruleWrite, error) {
	var body struct {
		ruleView
		Activation               string          `json:"activation"`
		ExpectedTargetObjectType string          `json:"expected_target_object_type"`
		Cron                     json.RawMessage `json:"cron"`
		MaxResults               json.RawMessage `json:"max_results"`
	}
	if err := DecodeStrict(raw, &body); err != nil {
		return ruleWrite{}, err
	}
	if kind := body.ExpectedTargetObjectType; kind != "" {
		if kind != objectOAuth && kind != objectProvider || body.TargetScope != "" && body.TargetScope != "single" || body.TargetGroup != "" ||
			body.AuthID == "" || body.Provider == "" || body.TargetAuthIndex == "" {
			return ruleWrite{}, fmt.Errorf("invalid_request: 来源核对需要指定单个对象、提供方、索引及有效对象类型")
		}
		body.Rule.expectedTargetObjectType = kind
	}
	if body.Cron != nil {
		var expression *string
		if err := json.Unmarshal(body.Cron, &expression); err != nil || expression == nil {
			return ruleWrite{}, fmt.Errorf("invalid_cron: expected a five-field expression")
		}
		// A previous editor sent an empty string alongside an unchanged fixed interval.
		if strings.TrimSpace(*expression) == "" && !(update && *expression == "" && body.IntervalSeconds > 0) {
			return ruleWrite{}, fmt.Errorf("invalid_cron: five fields required")
		}
		body.Rule.Cron = *expression
	}
	if body.MaxResults != nil {
		if err := json.Unmarshal(body.MaxResults, &body.Rule.MaxResults); err != nil || body.Rule.MaxResults < 1 || body.Rule.MaxResults > 200 {
			return ruleWrite{}, fmt.Errorf("invalid_request: max_results must be 1..200")
		}
	}
	return ruleWrite{body.Rule, body.Activation}, nil
}
