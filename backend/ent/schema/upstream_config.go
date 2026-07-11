package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UpstreamConfig stores shared upstream relay login/session configuration.
type UpstreamConfig struct {
	ent.Schema
}

func (UpstreamConfig) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_configs"},
	}
}

func (UpstreamConfig) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (UpstreamConfig) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100).NotEmpty(),
		field.String("provider").MaxLen(32).NotEmpty(),
		field.String("site_url").MaxLen(512).NotEmpty(),
		field.String("api_url").MaxLen(512).Optional().Nillable(),
		field.String("auth_mode").MaxLen(32).Default("user_login"),
		field.JSON("credentials", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("extra", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("proxy_id").Optional().Nillable(),
		field.Float("recharge_rate").
			Default(1).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("balance_to_cny_rate").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.String("status").MaxLen(20).Default(domain.StatusActive),
		field.String("last_error").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("last_checked_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_success_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UpstreamConfig) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("keys", UpstreamKey.Type),
		edge.To("accounts", Account.Type),
		edge.To("sync_results", UpstreamSyncResult.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("events", UpstreamEvent.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("incidents", UpstreamIncident.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("balance_snapshots", UpstreamBalanceSnapshot.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("usage_logs", UsageLog.Type),
		edge.To("proxy", Proxy.Type).
			Field("proxy_id").
			Unique(),
	}
}

func (UpstreamConfig) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider"),
		index.Fields("proxy_id"),
	}
}
