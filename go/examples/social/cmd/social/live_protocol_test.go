package main

import (
	"github.com/eleven-am/golem/go/examples/social/social"
)

func liveExtensions() map[string]any {
	return map[string]any{"golem": map[string]any{"protocol": 1, "contract": social.GolemGeneratedSchemaBundle().Contract().Fingerprint().String()}}
}
