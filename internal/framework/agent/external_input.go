package agent

// ExternalDelivery describes durable admission independently of the transport.
type ExternalDelivery struct {
	RecordID     string
	TargetThread string
	Queued       bool
	Delivered    bool
}
