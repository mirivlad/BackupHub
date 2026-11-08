package domain

import "fmt"

type JobState string

const (
 JobScheduled JobState = "scheduled"
 JobWaiting JobState = "waiting_for_source"
 JobQueued JobState = "queued"
 JobPreflight JobState = "preflight"
 JobPreparing JobState = "preparing"
 JobCapturing JobState = "capturing"
 JobCommitting JobState = "committing"
 JobVerifying JobState = "verifying"
 JobCompleted JobState = "completed"
 JobWarnings JobState = "completed_with_warnings"
 JobFailed JobState = "failed"
 JobCancelled JobState = "cancelled"
 JobInterrupted JobState = "interrupted"
 JobStale JobState = "stale"
)

var transitions = map[JobState][]JobState{
 JobScheduled:{JobWaiting,JobQueued,JobCancelled},
 JobWaiting:{JobQueued,JobCancelled,JobStale},
 JobQueued:{JobPreflight,JobCancelled,JobInterrupted},
 JobPreflight:{JobPreparing,JobFailed,JobCancelled,JobInterrupted,JobWaiting},
 JobPreparing:{JobCapturing,JobFailed,JobCancelled,JobInterrupted},
 JobCapturing:{JobCommitting,JobFailed,JobCancelled,JobInterrupted},
 JobCommitting:{JobVerifying,JobFailed,JobInterrupted},
 JobVerifying:{JobCompleted,JobWarnings,JobFailed,JobInterrupted},
 JobInterrupted:{JobQueued,JobFailed,JobCancelled},
 JobStale:{JobQueued,JobCancelled},
}

func ValidateTransition(from,to JobState) error {
 if from==to { return fmt.Errorf("duplicate transition %q",from) }
 for _,next := range transitions[from] { if next==to { return nil } }
 return fmt.Errorf("invalid job transition: %q -> %q",from,to)
}

func IsTerminal(state JobState) bool { return state==JobCompleted||state==JobWarnings||state==JobFailed||state==JobCancelled }
