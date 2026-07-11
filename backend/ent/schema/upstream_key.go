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

// UpstreamKey stores a key discovered or entered under an upstream config.
type UpstreamKey struct {
	ent.Schema
}

func (UpstreamKey) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_keys"},
	}
}

func (UpstreamKey) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (UpstreamKey) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("upstream_config_id"),
		field.String("name").MaxLen(100).Default(""),
		field.String("key").SchemaType(map[string]string{dialect.Postgres: "text"}).Sensitive(),
		field.String("key_hash").MaxLen(128).NotEmpty(),
		field.Int64("remote_key_id").Optional().Nillable(),
		field.Int64("upstream_group_id").Optional().Nillable(),
		field.String("upstream_group_name").MaxLen(100).Default(""),
		field.String("platform").MaxLen(50).Default("openai"),
		field.Float("rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Optional().
			Nillable(),
		field.String("status").MaxLen(20).Default(domain.StatusActive),
		field.Time("last_seen_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.JSON("extra", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

func (UpstreamKey) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("config", UpstreamConfig.Type).
			Ref("keys").
			Field("upstream_config_id").
			Required().
			Unique(),
		edge.To("accounts", Account.Type),
		edge.To("events", UpstreamEvent.Type),
		edge.To("incidents", UpstreamIncident.Type),
		edge.To("usage_logs", UsageLog.Type),
	}
}

func (UpstreamKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("upstream_config_id"),
		index.Fields("upstream_config_id", "key_hash"),
	}
}
