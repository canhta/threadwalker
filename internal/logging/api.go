package logging

// The OAuth and webhook server logs one event per request; the route is the
// Operation, a constant, never the URL.
const APIRequestFinished = "api.request.finished"
