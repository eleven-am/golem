package golem

import "fmt"

type IsolationLevel string

const (
	ReadCommitted IsolationLevel = "read_committed"
	Serializable  IsolationLevel = "serializable"
)

const maxTransactionAttempts = 10

type TransactionOption interface {
	applyTransactionOption(*transactionSettings) error
}

type transactionSettings struct {
	isolation    IsolationLevel
	attempts     int
	isolationSet bool
	attemptsSet  bool
}

type isolationOption IsolationLevel

func (option isolationOption) applyTransactionOption(settings *transactionSettings) error {
	level := IsolationLevel(option)
	if level != ReadCommitted && level != Serializable {
		return fmt.Errorf("P4_TRANSACTION_OPTION: isolation level %q is not supported", string(level))
	}
	if settings.isolationSet {
		return fmt.Errorf("P4_TRANSACTION_OPTION: isolation is set more than once")
	}
	settings.isolation, settings.isolationSet = level, true
	return nil
}

type retryConflictsOption int

func (option retryConflictsOption) applyTransactionOption(settings *transactionSettings) error {
	attempts := int(option)
	if attempts < 1 || attempts > maxTransactionAttempts {
		return fmt.Errorf("P4_TRANSACTION_OPTION: retry attempts must be between 1 and %d, got %d", maxTransactionAttempts, attempts)
	}
	if settings.attemptsSet {
		return fmt.Errorf("P4_TRANSACTION_OPTION: retry attempts are set more than once")
	}
	settings.attempts, settings.attemptsSet = attempts, true
	return nil
}

func Isolation(level IsolationLevel) TransactionOption {
	return isolationOption(level)
}

func RetryConflicts(attempts int) TransactionOption {
	return retryConflictsOption(attempts)
}

func RuntimeTransactionOptions(options []TransactionOption) (IsolationLevel, int, error) {
	return RuntimeTransactionOptionsWithDefault(options, ReadCommitted)
}

// RuntimeTransactionOptionsWithDefault preserves an explicitly authored
// isolation option while allowing the runtime's transaction owner to select
// the isolation used when that option is omitted.
func RuntimeTransactionOptionsWithDefault(options []TransactionOption, defaultIsolation IsolationLevel) (IsolationLevel, int, error) {
	if defaultIsolation != ReadCommitted && defaultIsolation != Serializable {
		return "", 0, fmt.Errorf("P4_TRANSACTION_OPTION: default isolation level %q is not supported", string(defaultIsolation))
	}
	settings := transactionSettings{isolation: defaultIsolation, attempts: 1}
	for index, option := range options {
		if option == nil {
			return "", 0, fmt.Errorf("P4_TRANSACTION_OPTION: option %d is nil", index)
		}
		if err := option.applyTransactionOption(&settings); err != nil {
			return "", 0, err
		}
	}
	return settings.isolation, settings.attempts, nil
}
