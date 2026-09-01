package graphql

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeLimitsNamesTheLimitAndBoundItRefuses(t *testing.T) {
	_, err := NormalizeLimits(Limits{MaxDepth: hardLimits.MaxDepth + 1})
	if err == nil {
		t.Fatal("a MaxDepth above the portable ceiling was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, "MaxDepth") {
		t.Fatalf("refusal does not name the limit: %q", message)
	}
	if !strings.Contains(message, "1..32") {
		t.Fatalf("refusal does not report the violated bound: %q", message)
	}
}

func TestLimitBoundsCoverEveryLimitField(t *testing.T) {
	probe := Limits{}
	fields := reflect.ValueOf(&probe).Elem()
	covered := make([]string, fields.NumField())
	for _, bound := range limitBounds {
		address := bound.field(&probe)
		index := -1
		for candidate := 0; candidate < fields.NumField(); candidate++ {
			if fields.Field(candidate).Kind() != reflect.Int {
				t.Fatalf("limit field %s is not an int", fields.Type().Field(candidate).Name)
			}
			if fields.Field(candidate).Addr().Interface().(*int) == address {
				index = candidate
				break
			}
		}
		if index < 0 {
			t.Fatalf("limit bound %s does not address a field of the Limits it was given", bound.name)
		}
		name := fields.Type().Field(index).Name
		if bound.name != name {
			t.Fatalf("limit bound named %s addresses field %s", bound.name, name)
		}
		if covered[index] != "" {
			t.Fatalf("field %s is covered by more than one limit bound", name)
		}
		covered[index] = name
	}
	for index, name := range covered {
		if name == "" {
			t.Fatalf("limit field %s has no bound and is never validated", fields.Type().Field(index).Name)
		}
	}
}
