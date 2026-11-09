package synthesis

// RoutingKey routes jobs to the work queue and failed jobs to the dead-letter queue.
const RoutingKey = "synthesize"

// Job is the message a worker receives from the work queue.
type Job struct {
	ID string `json:"jobId"`
	Request
}
