package operation

import (
	compileerr "github.com/eleven-am/golem/go/internal/bindingerr"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

func (c *Compiler) bindPageTake(operation compilerir.CustomOperationContractIR, arguments map[string]any) error {
	if operation.Pagination == nil {
		return nil
	}
	maximum := readir.NarrowCap(int(operation.Pagination.MaxTake), c.limits.Bind.MaxPageSize)
	value := int(operation.Pagination.DefaultTake)
	if supplied := arguments["take"]; supplied != nil {
		integer, ok := supplied.(int32)
		if !ok {
			return compileerr.Invariantf("page take has value %T", supplied)
		}
		value = int(integer)
	}
	if value < 1 || value > maximum {
		return compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerPositive, compileerr.Path("take"), compileerr.Model(operation.Name), compileerr.Bounds(1, maximum))
	}
	arguments["take"] = int32(value)
	return nil
}
