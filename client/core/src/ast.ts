import { Kind, type NameNode, type VariableDefinitionNode } from 'graphql'

export function nameNode(value: string): NameNode {
  return { kind: Kind.NAME, value }
}

export function variableDefinition(name: string, typeName: string): VariableDefinitionNode {
  return {
    kind: Kind.VARIABLE_DEFINITION,
    variable: { kind: Kind.VARIABLE, name: nameNode(name) },
    type: { kind: Kind.NAMED_TYPE, name: nameNode(typeName) },
  }
}
