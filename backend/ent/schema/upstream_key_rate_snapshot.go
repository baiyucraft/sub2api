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

// UpstreamKeyRateSnapshot stores an immutable, non-secret rate observation for one upstream key.
type UpstreamKeyRateSnapshot struct {
	ent.Schema
}

func (UpstreamKeyRateSnapshot) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "upstream_key_rate_snapshots"}}
}

func (UpstreamKeyRateSnapshot) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("upstream_config_id").Immutable(),
		field.Int64("upstream_key_id").Optional().Nillable().Immutable(),
		field.Int64("remote_key_id").Optional().Nillable().Immutable(),
		field.String("key_name_snapshot").MaxLen(100).Default("").Immutable(),
		field.String("key_hash_snapshot").MaxLen(128).Default("").Immutable(),
		field.Int64("sync_run_id").Optional().Nillable().Immutable(),
		field.String("provider").MaxLen(32).Immutable(),
		field.Float("raw_rate_multiplier").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Immutable(),
		field.Float("recharge_rate").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Immutable(),
		field.Float("effective_cost_multiplier").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Immutable(),
		field.String("source").MaxLen(32).Default("sync").Immutable(),
		field.Time("observed_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UpstreamKeyRateSnapshot) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("config", UpstreamConfig.Type).Ref("key_rate_snapshots").Field("upstream_config_id").Required().Unique().Immutable(),
		edge.From("key", UpstreamKey.Type).Ref("rate_snapshots").Field("upstream_key_id").Unique().Immutable(),
		edge.From("run", UpstreamSyncRun.Type).Ref("key_rate_snapshots").Field("sync_run_id").Unique().Immutable(),
	}
}

func (UpstreamKeyRateSnapshot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("upstream_key_id", "sync_run_id").Unique().
			Annotations(entsql.IndexWhere("upstream_key_id IS NOT NULL AND sync_run_id IS NOT NULL")),
		index.Fields("upstream_config_id", "observed_at"),
		index.Fields("upstream_key_id", "observed_at"),
		index.Fields("sync_run_id"),
	}
}
