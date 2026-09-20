package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ProxyIPGroupMember stores ordered membership between a proxy group and proxies.
type ProxyIPGroupMember struct {
	ent.Schema
}

func (ProxyIPGroupMember) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "proxy_ip_group_members"},
		field.ID("proxy_ip_group_id", "proxy_id"),
	}
}

func (ProxyIPGroupMember) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("proxy_ip_group_id"),
		field.Int64("proxy_id"),
		field.Int("position").Default(0).NonNegative(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (ProxyIPGroupMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("group", ProxyIPGroup.Type).
			Unique().Required().Field("proxy_ip_group_id").
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("proxy", Proxy.Type).
			Unique().Required().Field("proxy_id").
			Annotations(entsql.OnDelete(entsql.Restrict)),
	}
}

func (ProxyIPGroupMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("proxy_id"),
		index.Fields("proxy_ip_group_id", "position", "proxy_id"),
	}
}
