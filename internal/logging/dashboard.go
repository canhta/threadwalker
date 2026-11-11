package logging

// Dashboard events use the common typed metadata allowlist. Request URLs,
// headers, database errors and projected content are never log attributes.
const (
	DashboardRequestFinished = "dashboard.request.finished"
	DashboardServerStarted   = "dashboard.server.started"
	DashboardServerFinished  = "dashboard.server.finished"
)
