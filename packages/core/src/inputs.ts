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
      (f) => f.kind === 'object' && f.relationName === field.relationName && f.type === model.name,
    );
    if (!back) {
      throw new Error(`Relation ${model.name}.${field.name} has no back relation on ${field.type}`);
    }
    return back;
  }

  private writableFields(model: DatamodelModel, excludeRelation?: DatamodelField): DatamodelField[] {
    const hidden = this.ctx.hiddenFor(model.name);
    return model.fields.filter((field) => {
      if (field.isReadOnly || hidden.has(field.name)) {
        return false;
      }
      if (excludeRelation && field.name === excludeRelation.name) {
        return false;
      }
      return true;
    });
  }

  private updatableFields(model: DatamodelModel): DatamodelField[] {
    const immutable = this.ctx.immutableFor(model.name);
    return this.writableFields(model).filter((field) => !immutable.has(field.name));
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
    return new GraphQLInputObjectType({
      name,
      fields: () => {
        const fields: GraphQLInputFieldConfigMap = {};
        for (const field of this.writableFields(model, excludeRelation)) {
          if (field.kind === 'object') {
            if (!this.ctx.modelsByName.has(field.type)) {
              continue;
            }
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
    return this.memo(`${model.name}UpdateInput`, () =>
      new GraphQLInputObjectType({
        name: `${model.name}UpdateInput`,
        fields: () => {
          const fields: GraphQLInputFieldConfigMap = {};
          for (const field of this.updatableFields(model)) {
            if (field.kind === 'object') {
              if (!this.ctx.modelsByName.has(field.type)) {
                continue;
              }
              fields[field.name] = { type: this.nestedUpdateInput(field) };
            } else {
              fields[field.name] = this.scalarInputField(model, field, false);
            }
          }
          return fields;
        },
      }),
    );
  }

  private nestedUpdateInput(field: DatamodelField): GraphQLInputObjectType {
    const target = this.ctx.modelsByName.get(field.type)!;
    const whereUnique = this.ctx.whereUniqueInputs.get(target.name)!;
    if (field.isList) {
      return this.memo(`${target.name}UpdateManyRelationInput`, () =>
        new GraphQLInputObjectType({
          name: `${target.name}UpdateManyRelationInput`,
          fields: () => ({
            connect: { type: new GraphQLList(new GraphQLNonNull(whereUnique)) },
            disconnect: { type: new GraphQLList(new GraphQLNonNull(whereUnique)) },
          }),
        }),
      );
    }
    if (field.isRequired) {
      return this.memo(`${target.name}UpdateOneRequiredRelationInput`, () =>
        new GraphQLInputObjectType({
          name: `${target.name}UpdateOneRequiredRelationInput`,
          fields: () => ({
            connect: { type: whereUnique },
          }),
        }),
      );
    }
    return this.memo(`${target.name}UpdateOneRelationInput`, () =>
      new GraphQLInputObjectType({
        name: `${target.name}UpdateOneRelationInput`,
        fields: () => ({
          connect: { type: whereUnique },
          disconnect: { type: GraphQLBoolean },
        }),
      }),
    );
  }

  updateManyInput(model: DatamodelModel): GraphQLInputObjectType {
    return this.memo(`${model.name}UpdateManyInput`, () =>
      new GraphQLInputObjectType({
        name: `${model.name}UpdateManyInput`,
        fields: () => {
          const fields: GraphQLInputFieldConfigMap = {};
          for (const field of this.updatableFields(model)) {
            if (field.kind === 'object') {
              continue;
            }
            fields[field.name] = this.scalarInputField(model, field, false);
          }
          return fields;
        },
      }),
    );
  }
}
