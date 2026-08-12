package p8mutation

import "time"

type Patch struct {
	Path   string
	Before string
	After  string
}

type Gate struct {
	Directory        string
	Package          string
	Test             string
	Required         []string
	WorkspaceModules []string
}

type Mutation struct {
	Label   string
	Summary string
	Patches []Patch
	Gate    Gate
	Timeout time.Duration
}

type Status string

const (
	StatusKilled   Status = "KILLED"
	StatusSurvived Status = "SURVIVED"
	StatusInvalid  Status = "INVALID"
)

type Result struct {
	FormatVersion       int    `json:"formatVersion"`
	Mutation            string `json:"mutation"`
	Status              Status `json:"status"`
	Test                string `json:"test"`
	Duration            string `json:"duration"`
	Detail              string `json:"detail,omitempty"`
	BaselineEventSHA256 string `json:"baselineEventSHA256,omitempty"`
	MutantEventSHA256   string `json:"mutantEventSHA256,omitempty"`
	OutputSHA256        string `json:"outputSHA256,omitempty"`
}
