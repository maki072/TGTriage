package webapp

import "tgtriage/internal/domain"

// statusFilters/priorityFilters mirror the bot's own filter chips (see delivery/telegram/render.go)
// so the Mini App and the bot UI offer identical views of the same data.
var statusFilters = map[string][]domain.TaskStatus{
	"act":  {domain.StatusNew, domain.StatusInProgress},
	"new":  {domain.StatusNew},
	"wrk":  {domain.StatusInProgress},
	"snz":  {domain.StatusSnoozed},
	"done": {domain.StatusDone},
	"fp":   {domain.StatusFalsePositive},
	"all":  nil,
}

var priorityFilters = map[string][]domain.Priority{
	"all":  nil,
	"urg":  {domain.PriorityCritical, domain.PriorityHigh},
	"crit": {domain.PriorityCritical},
	"high": {domain.PriorityHigh},
	"med":  {domain.PriorityMedium},
	"low":  {domain.PriorityLow},
}
