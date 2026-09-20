// Package bus is the channel message bus: it routes inbound/outbound
// chat messages between IM channels (feishu, discord, telegram, ...)
// and the agent loop, with streaming delegation and backpressure.
//
// Not to be confused with pkg/events — the process-local runtime
// event bus for component observability. This package moves user
// conversations; that one moves telemetry.
package bus
