package temporal

import (
	"time"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func Precision(typ compilerir.LogicalTypeIR) uint16 {
	if typ.Kind == compilerir.TypeDate {
		return 0
	}
	if typ.Precision == nil || *typ.Precision > 6 {
		return 6
	}
	return *typ.Precision
}

func Truncate(value time.Time, precision uint16) time.Time {
	quantum := int64(1)
	for index := precision; index < 9; index++ {
		quantum *= 10
	}
	nanos := int64(value.Nanosecond())
	return time.Unix(value.Unix(), nanos/quantum*quantum).UTC()
}

func TruncateMicroseconds(microseconds int64, precision uint16) int64 {
	quantum := int64(1)
	for index := precision; index < 6; index++ {
		quantum *= 10
	}
	remainder := microseconds % quantum
	if remainder < 0 {
		remainder += quantum
	}
	return microseconds - remainder
}
