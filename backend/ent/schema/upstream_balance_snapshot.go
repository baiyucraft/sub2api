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

// UpstreamBalanceSnapshot stores an immutable upstream balance observation.
type UpstreamBalanceSnapshot struct {
	ent.Schema
}

func (UpstreamBalanceSnapshot) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_balance_snapshots"},
	}
}

func (UpstreamBalanceSnapshot) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("upstream_config_id").Immutable(),
		field.Int64("sync_run_id").Optional().Nillable().Immutable(),
		field.String("provider").MaxLen(32).Immutable(),
		field.Float("balance_raw").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("used_raw").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("total_raw").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("balance_cny").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("used_cny").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("total_recharged_cny").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("recharge_rate").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Int("balance_formula_version").Default(1).Immutable(),
		field.String("currency_source").MaxLen(16).Default("").Immutable(),
		field.Float("currency_to_cny_rate").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.String("currency_rate_source").MaxLen(32).Default("").Immutable(),
		field.Time("observed_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.JSON("metadata", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("created_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UpstreamBalanceSnapshot) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("config", UpstreamConfig.Type).
			Ref("balance_snapshots").
			Field("upstream_config_id").
			Required().
			Unique().
			Immutable(),
		edge.From("run", UpstreamSyncRun.Type).
			Ref("balance_snapshots").
			Field("sync_run_id").
			Unique().
			Immutable(),
	}
}

func (UpstreamBalanceSnapshot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("upstream_config_id", "observed_at"),
		index.Fields("sync_run_id"),
	}
}
