package fact

import "fmt"

const (
	formatVersionV1 uint16 = 1
	formatVersionV2 uint16 = 2
)

type FormatRetiredError struct {
	version uint16
}

func FormatRetired(version uint16) *FormatRetiredError {
	return &FormatRetiredError{version: version}
}

func (failure *FormatRetiredError) Version() uint16 { return failure.version }

func (failure *FormatRetiredError) Error() string {
	return fmt.Sprintf("GOLEM_EVENT_FORMAT_RETIRED: stored event fact (format V%d) is no longer readable: drain undelivered events on go/v0.8.0 before upgrading", failure.version)
}
