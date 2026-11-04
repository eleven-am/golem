import type { DocumentNode } from 'graphql'
import type { DocumentDecoration } from 'gql.tada'

export type GolemDocument = DocumentNode | string

export type ResultFor<T> = T extends DocumentDecoration<infer Result, infer _Variables> ? Result : unknown

export type VariablesFor<T> = T extends DocumentDecoration<infer _Result, infer Variables> ? Variables : Readonly<Record<string, unknown>>
