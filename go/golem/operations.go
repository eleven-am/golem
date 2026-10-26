package golem

type Operations struct{ _ operationsMarker }
type operationsMarker struct{}

func TxOperation[Operation any](_ *Operations, _ string, _ Operation) {}
