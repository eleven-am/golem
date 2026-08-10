import { ArgumentsHost, Catch } from '@nestjs/common';
import { GqlExceptionFilter } from '@nestjs/graphql';
import { GraphQLError } from 'graphql';
import { GolemError } from '@eleven-am/golem-core';

@Catch(GolemError)
export class GolemGraphQLExceptionFilter implements GqlExceptionFilter {
  catch(error: GolemError, _host: ArgumentsHost): GraphQLError {
    return new GraphQLError(error.message, { extensions: { code: error.code } });
  }
}
