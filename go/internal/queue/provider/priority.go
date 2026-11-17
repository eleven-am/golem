package provider

import "fmt"

func ValidatePriority(priority int) error {
	if priority != 0 && priority != 1 {
		return fmt.Errorf("QUEUE_PRIORITY_INVALID: priority must be 0 or 1")
	}
	return nil
}
