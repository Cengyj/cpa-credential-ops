package quality

// ManagementRoute is the native wire declaration of an exact authenticated plugin route.
type ManagementRoute struct {
	Method string
	Path   string
}

func ManagementRoutes() []ManagementRoute {
	return []ManagementRoute{
		{"GET", "/connection"}, {"PUT", "/connection"}, {"DELETE", "/connection"},
		{"GET", "/presets"}, {"POST", "/presets"}, {"DELETE", "/presets"},
		{"GET", "/targets"}, {"GET", "/questions"},
		{"GET", "/runs"}, {"GET", "/runs/detail"}, {"POST", "/runs"}, {"DELETE", "/runs"}, {"POST", "/runs/cancel"}, {"POST", "/runs/review"},
		{"DELETE", "/runs/history"},
		{"GET", "/rules"}, {"POST", "/rules"}, {"PUT", "/rules"}, {"DELETE", "/rules"}, {"POST", "/rules/run"}, {"POST", "/rules/pause"},
		{"PUT", "/rules/scheduling"}, {"PUT", "/rules/timer"}, {"GET", "/rules/question-preview"}, {"POST", "/rules/scope-preview"},
		{"POST", "/rules/handling/acknowledge"},
		{"DELETE", "/rules/run-history"},
		{"GET", "/metrics"}, {"GET", "/history"},
		{"GET", "/actions"}, {"POST", "/actions/preview"}, {"POST", "/actions/release"},
		{"GET", "/settings"}, {"PUT", "/settings"},
	}
}
