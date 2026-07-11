package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UpstreamIncident tracks a recoverable upstream operational incident.
type UpstreamIncident struct {
	ent.Schema
}

func (UpstreamIncident) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_incidents"},
	}
}

func (UpstreamIncident) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (UpstreamIncident) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("upstream_config_id"),
		field.Int64("upstream_key_id").Optional().Nillable(),
		field.Int64("source_event_id").Optional().Nillable(),
		field.String("incident_key").MaxLen(128),
		field.String("incident_type").MaxLen(64),
		field.String("severity").MaxLen(20),
		field.String("status").MaxLen(20).Default("open"),
		field.String("title").MaxLen(255),
		field.String("description").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Float("metric_value").Optional().Nillable(),
		field.Float("threshold_value").Optional().Nillable(),
		field.JSON("details", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int("occurrence_count").Default(1),
		field.Time("first_seen_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_seen_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("acknowledged_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("resolved_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UpstreamIncident) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("config", UpstreamConfig.Type).
			Ref("incidents").
			Field("upstream_config_id").
			Required().
			Unique(),
		edge.From("source_event", UpstreamEvent.Type).
			Ref("incidents").
			Field("source_event_id").
			Unique(),
		edge.From("key", UpstreamKey.Type).Ref("incidents").Field("upstream_key_id").Unique(),
	}
}

func (UpstreamIncident) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("upstream_config_id", "incident_key").Unique(),
		index.Fields("status", "severity", "last_seen_at"),
		index.Fields("upstream_key_id"),
		index.Fields("source_event_id"),
	}
}
