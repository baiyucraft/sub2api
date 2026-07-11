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

// UpstreamSyncResult records an immutable item-level synchronization result.
type UpstreamSyncResult struct {
	ent.Schema
}

func (UpstreamSyncResult) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_sync_results"},
	}
}

func (UpstreamSyncResult) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("sync_run_id").Immutable(),
		field.Int64("upstream_config_id").Immutable(),
		field.String("config_name").MaxLen(100).Immutable(),
		field.String("provider").MaxLen(32).Immutable(),
		field.String("status").MaxLen(20).Immutable(),
		field.String("stage").MaxLen(32).Default("").Immutable(),
		field.String("error_code").MaxLen(32).Default("").Immutable(),
		field.String("safe_message").Optional().Nillable().Immutable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Bool("retryable").Default(false).Immutable(),
		field.Int("http_status").Optional().Nillable().Immutable(),
		field.Int("remote_key_count").Default(0).Immutable(),
		field.Int("persisted_key_count").Default(0).Immutable(),
		field.Int("fallback_key_count").Default(0).Immutable(),
		field.Int("unresolved_key_count").Default(0).Immutable(),
		field.Int("updated_account_count").Default(0).Immutable(),
		field.JSON("warnings", []string{}).Default(func() []string { return []string{} }).Immutable().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("duration_ms").Default(0).Immutable(),
		field.Time("started_at").Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("finished_at").Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UpstreamSyncResult) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("run", UpstreamSyncRun.Type).
			Ref("results").
			Field("sync_run_id").
			Required().
			Unique().
			Immutable(),
		edge.From("config", UpstreamConfig.Type).
			Ref("sync_results").
			Field("upstream_config_id").
			Required().
			Unique().
			Immutable(),
	}
}

func (UpstreamSyncResult) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("sync_run_id", "upstream_config_id").Unique(),
		index.Fields("sync_run_id"),
		index.Fields("upstream_config_id", "created_at"),
		index.Fields("status", "created_at"),
	}
}
