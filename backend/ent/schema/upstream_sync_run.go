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

// UpstreamSyncRun records one upstream synchronization execution.
type UpstreamSyncRun struct {
	ent.Schema
}

func (UpstreamSyncRun) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_sync_runs"},
	}
}

func (UpstreamSyncRun) Mixin() []ent.Mixin {
	return nil
}

func (UpstreamSyncRun) Fields() []ent.Field {
	return []ent.Field{
		field.String("trigger").MaxLen(32),
		field.String("status").MaxLen(20).Default("pending"),
		field.Time("started_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("finished_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("total_configs").Default(0),
		field.Int("success_configs").Default(0),
		field.Int("partial_configs").Default(0),
		field.Int("failed_configs").Default(0),
		field.Time("created_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UpstreamSyncRun) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("results", UpstreamSyncResult.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("events", UpstreamEvent.Type),
		edge.To("balance_snapshots", UpstreamBalanceSnapshot.Type),
		edge.To("key_rate_snapshots", UpstreamKeyRateSnapshot.Type).
			Annotations(entsql.OnDelete(entsql.SetNull)),
	}
}

func (UpstreamSyncRun) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "started_at"),
		index.Fields("started_at"),
	}
}
