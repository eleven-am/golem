package sql

import (
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func RebasePlaceholders(text string, offset int, provider ir.Provider) string {
	if offset == 0 {
		return text
	}
	marker := byte('$')
	if provider == ir.ProviderSQLite {
		marker = '?'
	} else if provider != ir.ProviderPostgreSQL {
		return text
	}
	var result strings.Builder
	for index := 0; index < len(text); {
		if text[index] != marker || index+1 >= len(text) || text[index+1] < '0' || text[index+1] > '9' {
			result.WriteByte(text[index])
			index++
			continue
		}
		end := index + 1
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		position, _ := strconv.Atoi(text[index+1 : end])
		result.WriteByte(marker)
		result.WriteString(strconv.Itoa(position + offset))
		index = end
	}
	return result.String()
}
