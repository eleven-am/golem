package ir

type ListBoundFault uint8

const (
	ListBoundMissing ListBoundFault = iota + 1
	ListBoundStray
)

type ListBoundViolation struct {
	Fault ListBoundFault
	Level int
}

func (value GraphQLTypeIR) ListDepth() int {
	depth := 0
	for current := &value; current != nil && current.Kind == GraphQLTypeList; current = current.Element {
		depth++
	}
	return depth
}

func (value GraphQLTypeIR) ListMaxima() []uint32 {
	var result []uint32
	for current := &value; current != nil && current.Kind == GraphQLTypeList; current = current.Element {
		result = append(result, current.MaxItems)
	}
	return result
}

func (value GraphQLTypeIR) WithListMaxima(maxima []uint32) GraphQLTypeIR {
	if value.Kind != GraphQLTypeList || len(maxima) == 0 {
		return value
	}
	value.MaxItems = maxima[0]
	if value.Element != nil {
		element := value.Element.WithListMaxima(maxima[1:])
		value.Element = &element
	}
	return value
}

func (value GraphQLTypeIR) ResultListBoundViolations() []ListBoundViolation {
	return value.listBoundViolations(true, 0, nil)
}

func (value GraphQLTypeIR) ArgumentListBoundViolations() []ListBoundViolation {
	return value.listBoundViolations(false, 0, nil)
}

func (value GraphQLTypeIR) listBoundViolations(result bool, level int, found []ListBoundViolation) []ListBoundViolation {
	if value.Kind == GraphQLTypeList {
		switch {
		case !result && value.MaxItems != 0:
			found = append(found, ListBoundViolation{Fault: ListBoundStray, Level: level})
		case result && value.MaxItems == 0:
			found = append(found, ListBoundViolation{Fault: ListBoundMissing, Level: level})
		}
		if value.Element != nil {
			return value.Element.listBoundViolations(result, level+1, found)
		}
		return found
	}
	if value.MaxItems != 0 {
		found = append(found, ListBoundViolation{Fault: ListBoundStray, Level: level})
	}
	return found
}
