package golem

import "testing"

func TestRuntimeTransactionOptionsWithDefaultPreservesExplicitIsolation(t *testing.T) {
	for _, fallback := range []IsolationLevel{ReadCommitted, Serializable} {
		level, attempts, err := RuntimeTransactionOptionsWithDefault(nil, fallback)
		if err != nil || level != fallback || attempts != 1 {
			t.Fatalf("default=%q: level=%q attempts=%d err=%v", fallback, level, attempts, err)
		}
		for _, explicit := range []IsolationLevel{ReadCommitted, Serializable} {
			level, attempts, err = RuntimeTransactionOptionsWithDefault([]TransactionOption{Isolation(explicit), RetryConflicts(3)}, fallback)
			if err != nil || level != explicit || attempts != 3 {
				t.Fatalf("default=%q explicit=%q: level=%q attempts=%d err=%v", fallback, explicit, level, attempts, err)
			}
		}
	}
}

func TestRuntimeTransactionOptionsWithDefaultRefusesMalformedDefaultsAndOptions(t *testing.T) {
	for _, fallback := range []IsolationLevel{"", "repeatable_read"} {
		if level, attempts, err := RuntimeTransactionOptionsWithDefault(nil, fallback); err == nil || level != "" || attempts != 0 {
			t.Fatalf("invalid default=%q: level=%q attempts=%d err=%v", fallback, level, attempts, err)
		}
	}
	for _, options := range [][]TransactionOption{{nil}, {Isolation(ReadCommitted), Isolation(Serializable)}, {RetryConflicts(0)}, {RetryConflicts(2), RetryConflicts(2)}} {
		if level, attempts, err := RuntimeTransactionOptionsWithDefault(options, Serializable); err == nil || level != "" || attempts != 0 {
			t.Fatalf("invalid options: level=%q attempts=%d err=%v", level, attempts, err)
		}
	}
}
