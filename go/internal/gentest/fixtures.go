package gentest

import "github.com/eleven-am/golem/go/internal/compiler/ir"

const socialPackagePath = "example.test/social/internal/social"

const (
	socialSchemaID ir.SchemaID = "10000000000000000000000000000000"
	userModelID    ir.ModelID  = "20000000000000000000000000000000"
	postModelID    ir.ModelID  = "30000000000000000000000000000000"

	userIDFieldID        ir.FieldID = "21000000000000000000000000000000"
	userHandleFieldID    ir.FieldID = "22000000000000000000000000000000"
	userCreatedAtFieldID ir.FieldID = "23000000000000000000000000000000"
	userPostsFieldID     ir.FieldID = "24000000000000000000000000000000"
	postIDFieldID        ir.FieldID = "31000000000000000000000000000000"
	postAuthorIDFieldID  ir.FieldID = "32000000000000000000000000000000"
	postTitleFieldID     ir.FieldID = "33000000000000000000000000000000"
	postBodyFieldID      ir.FieldID = "34000000000000000000000000000000"
	postCreatedAtFieldID ir.FieldID = "35000000000000000000000000000000"
	postAuthorFieldID    ir.FieldID = "36000000000000000000000000000000"

	postAuthorRelationID ir.RelationID = "40000000000000000000000000000000"
)

// SocialCompilationIR returns a fresh canonical two-model fixture matching the
// first P1 vertical slice. IDs are fixed fixture values; gentest deliberately
// does not implement or reproduce compiler identity derivation.
func SocialCompilationIR() ir.CompilationIR {
	return ir.CompilationIR{
		Model: ir.ModelIR{
			FormatVersion: ir.ModelFormatVersion,
			Schema: ir.SchemaIdentityIR{
				ID:           socialSchemaID,
				StableName:   "social",
				PackagePath:  socialPackagePath,
				RootFunction: "DefineSchema",
				Actor: ir.GoNamedTypeIR{
					PackagePath: socialPackagePath,
					Name:        "Actor",
				},
			},
			Providers: []ir.Provider{ir.SQLite, ir.PostgreSQL},
			Models: []ir.ModelDeclIR{
				socialUserModel(),
				socialPostModel(),
			},
			Relations: []ir.RelationIR{socialPostAuthorRelation()},
		},
		Contract: ir.ContractIR{
			FormatVersion: ir.ContractFormatVersion,
			Models: []ir.ModelContractIR{
				socialUserContract(),
				socialPostContract(),
			},
			Methods: []ir.AttachedMethodIR{
				{
					ModelID:  modelIDPointer(userModelID),
					Receiver: ir.GoNamedTypeIR{PackagePath: socialPackagePath, Name: "User"},
					Name:     "DefinePolicy",
					Kind:     "policy",
					Actor:    namedTypePointer(socialPackagePath, "Actor"),
				},
				{
					ModelID:  modelIDPointer(postModelID),
					Receiver: ir.GoNamedTypeIR{PackagePath: socialPackagePath, Name: "Post"},
					Name:     "DefinePolicy",
					Kind:     "policy",
					Actor:    namedTypePointer(socialPackagePath, "Actor"),
				},
				{
					ModelID:  modelIDPointer(postModelID),
					Receiver: ir.GoNamedTypeIR{PackagePath: socialPackagePath, Name: "Post"},
					Name:     "BeforeCreate",
					Kind:     "hook",
				},
			},
		},
	}
}

func socialUserModel() ir.ModelDeclIR {
	return ir.ModelDeclIR{
		ID:          userModelID,
		Go:          ir.GoNamedTypeIR{PackagePath: socialPackagePath, Name: "User"},
		LogicalName: "User",
		Table:       ir.TableBindingIR{PhysicalName: "users"},
		Fields: []ir.FieldIR{
			scalarField(userIDFieldID, "ID", "id", 0, ir.LogicalTypeIR{Kind: ir.TypeUUID}, false, nil, false),
			scalarField(userHandleFieldID, "Handle", "handle", 1, stringType(40), false, nil, false),
			scalarField(userCreatedAtFieldID, "CreatedAt", "created_at", 2, dateTimeType(6), false, applicationDefault(ir.DefaultNow), true),
			relationField(
				userPostsFieldID,
				"Posts",
				"posts",
				3,
				postAuthorRelationID,
				ir.RelationInverse,
				ir.RelationHasMany,
			),
		},
		PrimaryKey: &ir.KeyIR{
			ID:           "21100000000000000000000000000000",
			Kind:         ir.KeyPrimary,
			LogicalName:  "UserPrimary",
			PhysicalName: "pk_users",
			Fields:       []ir.FieldID{userIDFieldID},
		},
		Uniques: []ir.KeyIR{
			{
				ID:           "22100000000000000000000000000000",
				Kind:         ir.KeyUnique,
				LogicalName:  "UserHandle",
				PhysicalName: "uq_users_handle",
				Fields:       []ir.FieldID{userHandleFieldID},
			},
		},
	}
}

func socialPostModel() ir.ModelDeclIR {
	return ir.ModelDeclIR{
		ID:          postModelID,
		Go:          ir.GoNamedTypeIR{PackagePath: socialPackagePath, Name: "Post"},
		LogicalName: "Post",
		Table:       ir.TableBindingIR{PhysicalName: "posts"},
		Fields: []ir.FieldIR{
			scalarField(postIDFieldID, "ID", "id", 0, ir.LogicalTypeIR{Kind: ir.TypeUUID}, false, nil, false),
			scalarField(postAuthorIDFieldID, "AuthorID", "author_id", 1, ir.LogicalTypeIR{Kind: ir.TypeUUID}, false, nil, false),
			scalarField(postTitleFieldID, "Title", "title", 2, stringType(200), false, nil, false),
			scalarField(postBodyFieldID, "Body", "body", 3, stringType(0), true, nil, false),
			scalarField(postCreatedAtFieldID, "CreatedAt", "created_at", 4, dateTimeType(6), false, applicationDefault(ir.DefaultNow), true),
			relationField(
				postAuthorFieldID,
				"Author",
				"author",
				5,
				postAuthorRelationID,
				ir.RelationSource,
				ir.RelationBelongsTo,
			),
		},
		PrimaryKey: &ir.KeyIR{
			ID:           "31100000000000000000000000000000",
			Kind:         ir.KeyPrimary,
			LogicalName:  "PostPrimary",
			PhysicalName: "pk_posts",
			Fields:       []ir.FieldID{postIDFieldID},
		},
		Indexes: []ir.IndexIR{
			{
				ID:           "32100000000000000000000000000000",
				ModelID:      postModelID,
				PhysicalName: "idx_posts_author_created",
				Method:       ir.IndexBTree,
				Keys: []ir.IndexKeyIR{
					{Column: fieldIDPointer(postAuthorIDFieldID), Direction: ir.SortAsc, Nulls: ir.NullsDefault},
					{Column: fieldIDPointer(postCreatedAtFieldID), Direction: ir.SortAsc, Nulls: ir.NullsDefault},
				},
				Provider: ir.ProviderScopePortable,
			},
		},
	}
}

func socialPostAuthorRelation() ir.RelationIR {
	return ir.RelationIR{
		ID:           postAuthorRelationID,
		Name:         "PostAuthor",
		SourceModel:  postModelID,
		TargetModel:  userModelID,
		SourceField:  postAuthorFieldID,
		InverseField: fieldIDPointer(userPostsFieldID),
		Cardinality:  ir.RelationOne,
		LocalFields:  []ir.FieldID{postAuthorIDFieldID},
		RemoteFields: []ir.FieldID{userIDFieldID},
		ForeignKey: &ir.ForeignKeyIR{
			ID:           "41000000000000000000000000000000",
			PhysicalName: "fk_posts_author",
			OnUpdate:     ir.ActionNoAction,
			OnDelete:     ir.ActionNoAction,
			Match:        ir.MatchSimple,
			Deferrable:   ir.NotDeferrable,
		},
	}
}

func socialUserContract() ir.ModelContractIR {
	return ir.ModelContractIR{
		ModelID:     userModelID,
		GraphQLName: "User",
		Fields: []ir.FieldContractIR{
			visibleField(userIDFieldID, "ID"),
			visibleField(userHandleFieldID, "Handle"),
			visibleField(userCreatedAtFieldID, "CreatedAt"),
			visibleField(userPostsFieldID, "Posts"),
		},
		Selectors: []ir.SelectorContractIR{
			{KeyID: "21100000000000000000000000000000", Kind: ir.KeyPrimary, Name: "id", Fields: []ir.FieldID{userIDFieldID}},
			{KeyID: "22100000000000000000000000000000", Kind: ir.KeyUnique, Name: "handle", Fields: []ir.FieldID{userHandleFieldID}},
		},
		Operations: []ir.Operation{"findOne", "findMany", "create", "update", "delete"},
		Limits:     ir.LimitContractIR{MaxTake: 100},
		Exposed:    true,
	}
}

func socialPostContract() ir.ModelContractIR {
	model := socialPostModel()
	shape, err := ir.BuildEventSchemaShape(model, nil, []ir.FieldID{postIDFieldID, postAuthorIDFieldID, postTitleFieldID, postBodyFieldID, postCreatedAtFieldID})
	if err != nil {
		panic(err)
	}
	fingerprint, err := ir.EventSchemaFingerprint(shape)
	if err != nil {
		panic(err)
	}
	return ir.ModelContractIR{
		ModelID:     postModelID,
		GraphQLName: "Post",
		Fields: []ir.FieldContractIR{
			visibleField(postIDFieldID, "ID"),
			visibleField(postAuthorIDFieldID, "AuthorID"),
			visibleField(postTitleFieldID, "Title"),
			visibleField(postBodyFieldID, "Body"),
			visibleField(postCreatedAtFieldID, "CreatedAt"),
			visibleField(postAuthorFieldID, "Author"),
		},
		Selectors: []ir.SelectorContractIR{
			{KeyID: "31100000000000000000000000000000", Kind: ir.KeyPrimary, Name: "id", Fields: []ir.FieldID{postIDFieldID}},
		},
		Operations:    []ir.Operation{"findOne", "findMany", "create", "update", "delete", "updateMany", "deleteMany"},
		Subscriptions: true,
		Event: &ir.EventContractIR{
			PayloadTypeName: "PostEvent", MetadataFields: []string{"eventID", "type", "id", "entity", "causationID", "transactionOrdinal", "recordedAt"},
			DeleteSnapshotFull: true, Schema: shape, SchemaFingerprint: fingerprint,
		},
		Limits:  ir.LimitContractIR{MaxTake: 100},
		Exposed: true,
	}
}

func scalarField(
	id ir.FieldID,
	goName string,
	column ir.SQLIdentifier,
	order uint32,
	logicalType ir.LogicalTypeIR,
	nullable bool,
	defaultValue *ir.DefaultIR,
	readOnly bool,
) ir.FieldIR {
	return ir.FieldIR{
		ID:               id,
		GoName:           goName,
		LogicalName:      goName,
		DeclarationOrder: order,
		Kind:             ir.FieldScalar,
		Scalar: &ir.ScalarFieldIR{
			Column:           column,
			Type:             logicalType,
			Nullable:         nullable,
			Default:          defaultValue,
			DatabaseReadOnly: readOnly,
		},
	}
}

func relationField(
	id ir.FieldID,
	goName string,
	logicalName string,
	order uint32,
	relationID ir.RelationID,
	role ir.RelationEndpointRole,
	kind ir.RelationKind,
) ir.FieldIR {
	return ir.FieldIR{
		ID:               id,
		GoName:           goName,
		LogicalName:      logicalName,
		DeclarationOrder: order,
		Kind:             ir.FieldRelation,
		Relation: &ir.RelationFieldIR{
			RelationID: relationID,
			Role:       role,
			Kind:       kind,
		},
	}
}

func stringType(max uint32) ir.LogicalTypeIR {
	result := ir.LogicalTypeIR{Kind: ir.TypeString}
	if max > 0 {
		result.MaxLength = &max
	}
	return result
}

func dateTimeType(precision uint16) ir.LogicalTypeIR {
	return ir.LogicalTypeIR{Kind: ir.TypeDateTime, Precision: &precision}
}

func applicationDefault(kind ir.DefaultKind) *ir.DefaultIR {
	return &ir.DefaultIR{Kind: kind, Producer: ir.ProducerApplication}
}

func visibleField(id ir.FieldID, graphqlName string) ir.FieldContractIR {
	return ir.FieldContractIR{FieldID: id, GraphQLName: graphqlName, Modes: []ir.FieldMode{ir.ModeVisible}}
}

func fieldIDPointer(value ir.FieldID) *ir.FieldID { return &value }

func modelIDPointer(value ir.ModelID) *ir.ModelID { return &value }

func namedTypePointer(packagePath, name string) *ir.GoNamedTypeIR {
	return &ir.GoNamedTypeIR{PackagePath: packagePath, Name: name}
}
