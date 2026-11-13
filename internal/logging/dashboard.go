package logging

// Dashboard events use the common typed metadata allowlist. Request URLs,
// headers, database errors and projected content are never log attributes.
const (
	DashboardRequestFinished = "dashboard.request.finished"
	DashboardPageRendered    = "dashboard.page.rendered"
	DashboardRateLimited     = "dashboard.rate.limited"
	DashboardAuthFailed      = "dashboard.auth.failed"
	DashboardServerStarted   = "dashboard.server.started"
	DashboardServerFinished  = "dashboard.server.finished"
)
