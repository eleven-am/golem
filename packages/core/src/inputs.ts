import {
  GraphQLBoolean,
  GraphQLEnumType,
  GraphQLInputFieldConfigMap,
  GraphQLInputObjectType,
  GraphQLList,
  GraphQLNonNull,
  GraphQLScalarType,
} from 'graphql';
import { DatamodelField, DatamodelModel } from './datamodel';
import { ucFirst } from './naming';

export interface InputBuilderContext {
  modelsByName: Map<string, DatamodelModel>;
  enumTypes: Map<string, GraphQLEnumType>;
  whereUniqueInputs: Map<string, GraphQLInputObjectType>;
  scalarType: (model: DatamodelModel, field: DatamodelField) => GraphQLScalarType;
  hiddenFor: (model: string) => ReadonlySet<string>;
  immutableFor: (model: string) => ReadonlySet<string>;
  readOnlyFor: (model: string) => ReadonlySet<string>;
}

export class InputTypeRegistry {
  private readonly types = new Map<string, GraphQLInputObjectType>();

  constructor(private readonly ctx: InputBuilderContext) {}

  private memo(name: string, factory: () => GraphQLInputObjectType): GraphQLInputObjectType {
    const existing = this.types.get(name);
    if (existing) {
      return existing;
    }
    const created = factory();
    this.types.set(name, created);
    return created;
  }

  find(name: string): GraphQLInputObjectType | undefined {
    return this.types.get(name);
  }

  private backRelationField(model: DatamodelModel, field: DatamodelField): DatamodelField {
    const target = this.ctx.modelsByName.get(field.type);
    if (!target) {
      throw new Error(`Relation ${model.name}.${field.name} targets unknown model ${field.type}`);
    }
    const back = target.fields.find(
      (f) =>
        f !== field &&
        f.kind === 'object' &&
        f.relationName === field.relationName &&
        f.type === model.name,
    );
    if (!back) {
      throw new Error(`Relation ${model.name}.${field.name} has no back relation on ${field.type}`);
    }
    return back;
  }

  private writableFields(model: DatamodelModel, excludeRelation?: DatamodelField): DatamodelField[] {
    const hidden = this.ctx.hiddenFor(model.name);
    const readOnly = this.ctx.readOnlyFor(model.name);
    return model.fields.filter((field) => {
      if (field.isReadOnly || hidden.has(field.name) || readOnly.has(field.name)) {
        return false;
      }
      if (excludeRelation && field.name === excludeRelation.name) {
        return false;
      }
      return true;
    });
  }

  private supportedWriteFields(
    model: DatamodelModel,
    excludeRelation?: DatamodelField,
  ): DatamodelField[] {
    return this.writableFields(model, excludeRelation).filter(
      (field) => field.kind !== 'object' || this.ctx.modelsByName.has(field.type),
    );
  }

  private updatableFields(
    model: DatamodelModel,
    excludeRelation?: DatamodelField,
  ): DatamodelField[] {
    const immutable = this.ctx.immutableFor(model.name);
    return this.supportedWriteFields(model, excludeRelation).filter(
      (field) => !immutable.has(field.name),
    );
  }

  private requireFields(
    inputName: string,
    model: DatamodelModel,
    fields: readonly DatamodelField[],
  ): readonly DatamodelField[] {
    if (fields.length === 0) {
      throw new Error(
        `Input ${inputName} for model ${model.name} has no writable fields; adjust field access configuration or disable the operation`,
      );
    }
    return fields;
  }

  private scalarInputField(model: DatamodelModel, field: DatamodelField, required: boolean) {
    const base =
      field.kind === 'enum' ? this.ctx.enumTypes.get(field.type)! : this.ctx.scalarType(model, field);
    return { type: required ? new GraphQLNonNull(base) : base };
  }

  private createRequired(field: DatamodelField): boolean {
    return field.isRequired && !field.hasDefaultValue && !field.isUpdatedAt;
  }

  createInput(model: DatamodelModel): GraphQLInputObjectType {
    return this.memo(`${model.name}CreateInput`, () =>
      this.buildCreateInput(`${model.name}CreateInput`, model, undefined),
    );
  }

  private createWithoutInput(model: DatamodelModel, excludeRelation: DatamodelField): GraphQLInputObjectType {
    const name = `${model.name}CreateWithout${ucFirst(excludeRelation.name)}Input`;
    return this.memo(name, () => this.buildCreateInput(name, model, excludeRelation));
  }

  private buildCreateInput(
    name: string,
    model: DatamodelModel,
    excludeRelation: DatamodelField | undefined,
  ): GraphQLInputObjectType {
    const writable = this.requireFields(
      name,
      model,
      this.supportedWriteFields(model, excludeRelation),
    );
    return new GraphQLInputObjectType({
      name,
      fields: () => {
        const fields: GraphQLInputFieldConfigMap = {};
        for (const field of writable) {
          if (field.kind === 'object') {
            fields[field.name] = { type: this.nestedCreateInput(model, field) };
          } else {
            fields[field.name] = this.scalarInputField(model, field, this.createRequired(field));
          }
        }
        return fields;
      },
    });
  }

  private nestedCreateInput(model: DatamodelModel, field: DatamodelField) {
    const target = this.ctx.modelsByName.get(field.type)!;
    const back = this.backRelationField(model, field);
    const whereUnique = this.ctx.whereUniqueInputs.get(target.name)!;
    if (field.isList) {
      const envelope = this.memo(`${target.name}CreateNestedManyWithout${ucFirst(back.name)}Input`, () =>
        new GraphQLInputObjectType({
          name: `${target.name}CreateNestedManyWithout${ucFirst(back.name)}Input`,
          fields: () => ({
            create: { type: new GraphQLList(new GraphQLNonNull(this.createWithoutInput(target, back))) },
            connect: { type: new GraphQLList(new GraphQLNonNull(whereUnique)) },
          }),
        }),
      );
      return envelope;
    }
    const envelope = this.memo(`${target.name}CreateNestedOneWithout${ucFirst(back.name)}Input`, () =>
      new GraphQLInputObjectType({
        name: `${target.name}CreateNestedOneWithout${ucFirst(back.name)}Input`,
        fields: () => ({
          create: { type: this.createWithoutInput(target, back) },
          connect: { type: whereUnique },
        }),
      }),
    );
    return field.isRequired ? new GraphQLNonNull(envelope) : envelope;
  }

  updateInput(model: DatamodelModel): GraphQLInputObjectType {
    const name = `${model.name}UpdateInput`;
    const updatable = this.requireFields(name, model, this.updatableFields(model));
    return this.memo(`${model.name}UpdateInput`, () =>
      new GraphQLInputObjectType({
        name,
        fields: () => {
          const fields: GraphQLInputFieldConfigMap = {};
          for (const field of updatable) {
            if (field.kind === 'object') {
              fields[field.name] = { type: this.nestedUpdateInput(model, field) };
            } else {
              fields[field.name] = this.scalarInputField(model, field, false);
            }
          }
          return fields;
        },
      }),
    );
  }

  private updateWithoutInput(
    model: DatamodelModel,
    excludeRelation: DatamodelField,
  ): GraphQLInputObjectType {
    const name = `${model.name}UpdateWithout${ucFirst(excludeRelation.name)}Input`;
    const updatable = this.requireFields(
      name,
      model,
      this.updatableFields(model, excludeRelation),
    );
    return this.memo(
      name,
      () =>
        new GraphQLInputObjectType({
          name,
          fields: () => {
            const fields: GraphQLInputFieldConfigMap = {};
            for (const field of updatable) {
              if (field.kind === 'object') {
                fields[field.name] = { type: this.nestedUpdateInput(model, field) };
              } else {
                fields[field.name] = this.scalarInputField(model, field, false);
              }
            }
            return fields;
          },
        }),
    );
  }

  private nestedUpdateInput(
    model: DatamodelModel,
    field: DatamodelField,
  ): GraphQLInputObjectType {
    const target = this.ctx.modelsByName.get(field.type)!;
    const back = this.backRelationField(model, field);
    const whereUnique = this.ctx.whereUniqueInputs.get(target.name)!;
    const updateWithout = this.updateWithoutInput(target, back);
    if (field.isList) {
      const relationName = `${target.name}UpdateManyWithout${ucFirst(back.name)}Input`;
      const updateWithWhere = this.memo(
        `${target.name}UpdateWithWhereUniqueWithout${ucFirst(back.name)}Input`,
        () =>
          new GraphQLInputObjectType({
            name: `${target.name}UpdateWithWhereUniqueWithout${ucFirst(back.name)}Input`,
            fields: {
              where: { type: new GraphQLNonNull(whereUnique) },
              data: { type: new GraphQLNonNull(updateWithout) },
            },
          }),
      );
      return this.memo(relationName, () =>
        new GraphQLInputObjectType({
          name: relationName,
          fields: () => ({
            update: { type: new GraphQLList(new GraphQLNonNull(updateWithWhere)) },
            connect: { type: new GraphQLList(new GraphQLNonNull(whereUnique)) },
            disconnect: { type: new GraphQLList(new GraphQLNonNull(whereUnique)) },
          }),
        }),
      );
    }
    if (field.isRequired) {
      const relationName = `${target.name}UpdateOneRequiredWithout${ucFirst(back.name)}Input`;
      return this.memo(relationName, () =>
        new GraphQLInputObjectType({
          name: relationName,
          fields: () => ({
            update: { type: updateWithout },
            connect: { type: whereUnique },
          }),
        }),
      );
    }
    const relationName = `${target.name}UpdateOneWithout${ucFirst(back.name)}Input`;
    return this.memo(relationName, () =>
      new GraphQLInputObjectType({
        name: relationName,
        fields: () => ({
          update: { type: updateWithout },
          connect: { type: whereUnique },
          disconnect: { type: GraphQLBoolean },
        }),
      }),
    );
  }

  updateManyInput(model: DatamodelModel): GraphQLInputObjectType {
    const name = `${model.name}UpdateManyInput`;
    const updatable = this.requireFields(
      name,
      model,
      this.updatableFields(model).filter((field) => field.kind !== 'object'),
    );
    return this.memo(name, () =>
      new GraphQLInputObjectType({
        name,
        fields: () => {
          const fields: GraphQLInputFieldConfigMap = {};
          for (const field of updatable) {
            fields[field.name] = this.scalarInputField(model, field, false);
          }
          return fields;
        },
      }),
    );
  }
}
