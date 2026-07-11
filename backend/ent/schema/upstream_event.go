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

// UpstreamEvent stores append-only upstream operational events.
type UpstreamEvent struct {
	ent.Schema
}

func (UpstreamEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_events"},
	}
}

func (UpstreamEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("upstream_config_id").Immutable(),
		field.Int64("upstream_key_id").Optional().Nillable().Immutable(),
		field.Int64("sync_run_id").Optional().Nillable().Immutable(),
		field.Int64("account_id").Optional().Nillable().Immutable(),
		field.String("event_key").MaxLen(128).Optional().Nillable().Immutable(),
		field.String("event_type").MaxLen(64).Immutable(),
		field.String("severity").MaxLen(20).Immutable(),
		field.String("source").MaxLen(32).Immutable(),
		field.String("message").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.JSON("payload", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("occurred_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UpstreamEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("config", UpstreamConfig.Type).
			Ref("events").
			Field("upstream_config_id").
			Required().
			Unique().
			Immutable(),
		edge.From("key", UpstreamKey.Type).Ref("events").Field("upstream_key_id").Unique().Immutable(),
		edge.From("run", UpstreamSyncRun.Type).Ref("events").Field("sync_run_id").Unique().Immutable(),
		edge.From("account", Account.Type).Ref("upstream_events").Field("account_id").Unique().Immutable(),
		edge.To("incidents", UpstreamIncident.Type).
			Annotations(entsql.OnDelete(entsql.SetNull)),
	}
}

func (UpstreamEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("upstream_config_id", "event_key").
			Unique().
			Annotations(entsql.IndexWhere("event_key IS NOT NULL")),
		index.Fields("upstream_config_id", "occurred_at"),
		index.Fields("upstream_key_id", "occurred_at"),
		index.Fields("sync_run_id"),
		index.Fields("event_type", "occurred_at"),
	}
}
