package schema

import (
	"time"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ProxyBinding is the stable ID namespace shared by real proxies and groups.
// The SQL migration owns its cross-table constraints and insert triggers.
type ProxyBinding struct{ ent.Schema }

func (ProxyBinding) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "proxy_bindings"}}
}

func (ProxyBinding) Fields() []ent.Field {
	return []ent.Field{
		field.String("binding_type").MaxLen(20),
		field.Int64("proxy_id").Optional().Nillable(),
		field.Int64("proxy_ip_group_id").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (ProxyBinding) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("proxy_id").Unique(),
		index.Fields("proxy_ip_group_id").Unique(),
	}
}
