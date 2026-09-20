package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ProxyIPGroup defines an ordered pool of proxies that can be bound to an account.
type ProxyIPGroup struct {
	ent.Schema
}

func (ProxyIPGroup) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "proxy_ip_groups"}}
}

func (ProxyIPGroup) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}, mixins.SoftDeleteMixin{}}
}

func (ProxyIPGroup) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100).NotEmpty(),
		field.Int("per_ip_concurrency").Default(10).Positive(),
	}
}

func (ProxyIPGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique().Annotations(entsql.IndexWhere("deleted_at IS NULL")),
	}
}

func (ProxyIPGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("proxies", Proxy.Type).
			Through("members", ProxyIPGroupMember.Type),
		edge.From("accounts", Account.Type).Ref("proxy_ip_group"),
	}
}
