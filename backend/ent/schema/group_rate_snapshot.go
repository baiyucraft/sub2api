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

// GroupRateSnapshot stores an immutable public rate configuration version.
// It is intentionally separate from monitor probe history so rate trends can
// cover longer ranges without fabricating pre-deployment history.
type GroupRateSnapshot struct {
	ent.Schema
}

func (GroupRateSnapshot) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "group_rate_snapshots"},
	}
}

func (GroupRateSnapshot) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id"),
		field.Float("rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}),
		field.Bool("peak_rate_enabled").Default(false),
		field.String("peak_start").MaxLen(5).Default(""),
		field.String("peak_end").MaxLen(5).Default(""),
		field.Float("peak_rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1),
		field.String("timezone").MaxLen(64).Default("UTC"),
		field.Time("effective_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (GroupRateSnapshot) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("group", Group.Type).
			Ref("rate_snapshots").
			Field("group_id").
			Unique().
			Required(),
	}
}

func (GroupRateSnapshot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id", "effective_at").StorageKey("idx_group_rate_snapshots_group_effective"),
	}
}
