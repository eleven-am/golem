package runtime

const (
	compileErrorUserID    = "00000000-0000-0000-0000-000000000001"
	compileErrorPostID    = "00000000-0000-0000-0000-000000000002"
	compileErrorCommentID = "00000000-0000-0000-0000-000000000003"
)

func allCompileErrorCases() []compileErrorCase {
	return append(compileErrorCases(), writeCompileErrorCases()...)
}

func badUserInput(argument string) map[string]any {
	return map[string]any{"code": "BAD_USER_INPUT", "argument": argument}
}

func limitExceeded(limit string, maximum int) map[string]any {
	return map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": limit, "max": maximum}
}

func invalidValue(argument string) map[string]any {
	return map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": argument}
}

func writeCompileErrorCases() []compileErrorCase {
	createUser := `mutation($d: UserCreateInput!) { createUser(data: $d) { id } }`
	updateUser := `mutation($w: UserWhereUniqueInput!, $d: UserUpdateInput!) { updateUser(where: $w, data: $d) { id } }`
	unique := map[string]any{"id": compileErrorUserID}
	user := func(extra map[string]any) map[string]any {
		value := map[string]any{"id": compileErrorUserID, "name": "u"}
		for key, item := range extra {
			value[key] = item
		}
		return value
	}
	return []compileErrorCase{
		{name: "create-uuid-with-marker", query: createUser, variables: map[string]any{"d": user(map[string]any{"id": compileErrorMarker})},
			message: "Input data.id on User is not a valid UUID", path: []any{"createUser"}, extensions: invalidValue("data.id")},
		{name: "update-data-empty", query: updateUser, variables: map[string]any{"w": unique, "d": map[string]any{}},
			message: "Argument data on User must not be empty", path: []any{"updateUser"}, extensions: badUserInput("data")},
		{name: "update-where-empty-selector", query: updateUser, variables: map[string]any{"w": map[string]any{}, "d": map[string]any{"name": map[string]any{"set": "a"}}},
			message: "Argument where on User must set exactly one member", path: []any{"updateUser"}, extensions: badUserInput("where")},
		{name: "update-operation-empty", query: updateUser, variables: map[string]any{"w": unique, "d": map[string]any{"name": map[string]any{}}},
			message: "Input data.name on User must set exactly one member", path: []any{"updateUser"}, extensions: badUserInput("data.name")},
		{name: "update-operation-null", query: updateUser, variables: map[string]any{"w": unique, "d": map[string]any{"name": map[string]any{"set": nil}}},
			message: "Input data.name.set on User cannot be null", path: []any{"updateUser"}, extensions: badUserInput("data.name.set")},
		{name: "update-value-with-marker", query: updateUser, variables: map[string]any{"w": unique, "d": map[string]any{"id": map[string]any{"set": compileErrorMarker}}},
			message: "Input data.id.set on User is not a valid UUID", path: []any{"updateUser"}, extensions: invalidValue("data.id.set")},
		{name: "upsert-update-empty", query: `mutation($w: UserWhereUniqueInput!, $c: UserCreateInput!, $u: UserUpdateInput!) { upsertUser(where: $w, create: $c, update: $u) { id } }`,
			variables: map[string]any{"w": unique, "c": user(nil), "u": map[string]any{}},
			message:   "Argument update on User must not be empty", path: []any{"upsertUser"}, extensions: badUserInput("update")},
		{name: "update-many-where-empty", query: `mutation { updateManyUsers(where: {}, data: {name: {set: "a"}}) { count } }`,
			message: "Argument where on User must not be empty", path: []any{"updateManyUsers"}, extensions: badUserInput("where")},
		{name: "delete-many-and-empty", query: `mutation { deleteManyUsers(where: {AND: []}) { count } }`,
			message: "Input where.AND on User must not be empty", path: []any{"deleteManyUsers"}, extensions: badUserInput("where.AND")},
		{name: "nested-envelope-empty", query: createUser, variables: map[string]any{"d": user(map[string]any{"posts": map[string]any{}})},
			message: "Input data.posts on User must not be empty", path: []any{"createUser"}, extensions: badUserInput("data.posts")},
		{name: "nested-list-empty", query: createUser, variables: map[string]any{"d": user(map[string]any{"posts": map[string]any{"create": []any{}}})},
			message: "Input data.posts.create on User must not be empty", path: []any{"createUser"}, extensions: badUserInput("data.posts.create")},
		{name: "nested-operation-null", query: createUser, variables: map[string]any{"d": user(map[string]any{"posts": map[string]any{"create": nil}})},
			message: "Input data.posts.create on User cannot be null", path: []any{"createUser"}, extensions: badUserInput("data.posts.create")},
		{name: "nested-connect-empty-selector", query: createUser, variables: map[string]any{"d": user(map[string]any{"posts": map[string]any{"connect": []any{map[string]any{}}}})},
			message: "Input data.posts.connect[0] on Post must set exactly one member", path: []any{"createUser"}, extensions: badUserInput("data.posts.connect[0]")},
		{name: "nested-list-items", query: createUser, variables: map[string]any{"d": user(map[string]any{"posts": map[string]any{"create": []any{
			map[string]any{"id": compileErrorPostID, "title": "a"}, map[string]any{"id": compileErrorPostID, "title": "b"},
			map[string]any{"id": compileErrorPostID, "title": "c"}, map[string]any{"id": compileErrorPostID, "title": "d"}}}})},
			message: "Input exceeds MaxListItems (3)", path: []any{"createUser"}, extensions: limitExceeded("MaxListItems", 3)},
		{name: "input-nodes", query: createUser, variables: map[string]any{"d": user(map[string]any{"posts": map[string]any{"create": []any{
			map[string]any{"id": compileErrorPostID, "title": "a"}, map[string]any{"id": compileErrorPostID, "title": "b"}, map[string]any{"id": compileErrorPostID, "title": "c"}}}})},
			message: "Input exceeds MaxInputNodes (6)", path: []any{"createUser"}, extensions: limitExceeded("MaxInputNodes", 6)},
	}
}
