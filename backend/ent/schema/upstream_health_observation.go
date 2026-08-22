package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UpstreamHealthObservation stores immutable, non-secret health evidence for
// one upstream key. It intentionally has no Ent edges: observations outlive
// soft-deleted accounts/keys for the bounded retention window and are queried
// by their snapshot identifiers.
type UpstreamHealthObservation struct {
	ent.Schema
}

func (UpstreamHealthObservation) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "upstream_health_observations"}}
}

func (UpstreamHealthObservation) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("upstream_config_id").Immutable(),
		field.Int64("upstream_key_id").Immutable(),
		field.Int64("account_id").Optional().Nillable().Immutable(),
		field.String("platform").MaxLen(50).Default("").Immutable(),
		field.String("model").MaxLen(255).Default("").Immutable(),
		field.String("protocol").MaxLen(50).Default("").Immutable(),
		field.String("source").MaxLen(20).Default("probe").Immutable(),
		field.String("state").MaxLen(20).Immutable(),
		field.String("result").MaxLen(100).Default("").Immutable(),
		field.String("reason").SchemaType(map[string]string{dialect.Postgres: "text"}).Default("").Immutable(),
		field.Int("http_status").Optional().Nillable().Immutable(),
		field.Int64("ttft_ms").Optional().Nillable().Immutable(),
		field.Int64("duration_ms").Optional().Nillable().Immutable(),
		field.Int64("input_tokens").Optional().Nillable().Immutable(),
		field.Int64("output_tokens").Optional().Nillable().Immutable(),
		field.Float("output_tps").Optional().Nillable().Immutable(),
		field.Int("confidence_score").Optional().Nillable().Immutable(),
		field.String("confidence_prompt_version").MaxLen(64).Optional().Nillable().Immutable(),
		field.String("requested_effort").MaxLen(32).Optional().Nillable().Immutable(),
		field.Int64("reasoning_tokens").Optional().Nillable().Immutable(),
		field.JSON("confidence_checks", map[string]int{}).Optional().Immutable(),
		field.String("confidence_status").MaxLen(32).Optional().Nillable().Immutable(),
		field.Time("observed_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UpstreamHealthObservation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("upstream_key_id", "observed_at"),
		index.Fields("upstream_config_id", "observed_at"),
		index.Fields("observed_at"),
	}
}
