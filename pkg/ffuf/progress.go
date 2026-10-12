package ffuf

import (
	"time"
)

type Progress struct {
	StartedAt time.Time
	ReqCount  int
	// CompletedCount is the number of logical inputs that reached a terminal
	// outcome in the current progress cycle. Retries do not increment it.
	CompletedCount int
	ResponseCount  int
	ReqTotal       int
	ReqSec         int64
	QueuePos       int
	QueueTotal     int
	ErrorCount     int
}
