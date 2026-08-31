package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AccountProxyBinding stores the ordered proxy routes assigned to an account.
// The legacy accounts.proxy_id column remains the first route for compatibility.
type AccountProxyBinding struct {
	ent.Schema
}

func (AccountProxyBinding) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "account_proxy_bindings"},
		field.ID("account_id", "proxy_id"),
	}
}

func (AccountProxyBinding) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("account_id"),
		field.Int64("proxy_id"),
		field.Int("position").NonNegative(),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (AccountProxyBinding) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("account", Account.Type).
			Unique().
			Required().
			Field("account_id").
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("proxy", Proxy.Type).
			Unique().
			Required().
			Field("proxy_id").
			Annotations(entsql.OnDelete(entsql.Restrict)),
	}
}

func (AccountProxyBinding) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account_id", "position"),
		index.Fields("proxy_id"),
	}
}
