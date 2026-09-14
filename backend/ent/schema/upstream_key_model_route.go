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

// UpstreamKeyModelRoute records the model-level provider capability of an
// upstream key. A key remains a single physical account; this table only
// describes which public model should be sent to which concrete platform.
type UpstreamKeyModelRoute struct {
	ent.Schema
}

func (UpstreamKeyModelRoute) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "upstream_key_model_routes"}}
}

func (UpstreamKeyModelRoute) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}, mixins.SoftDeleteMixin{}}
}

func (UpstreamKeyModelRoute) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("upstream_key_id"),
		field.String("public_model").MaxLen(200).NotEmpty(),
		field.String("upstream_model").MaxLen(200).Default(""),
		// Empty target_platform is intentional for unknown/ambiguous models.
		// Such rows are retained for diagnostics but are never schedulable.
		field.String("target_platform").MaxLen(50).Default(""),
		field.String("api_protocol").MaxLen(50).Default(""),
		field.String("source").MaxLen(16).Default("auto"),
		field.Bool("enabled").Default(true),
		field.Int("priority").Default(100),
		field.String("status").MaxLen(20).Default("available"),
		field.Time("last_seen_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("last_error").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}

func (UpstreamKeyModelRoute) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("key", UpstreamKey.Type).
			Ref("model_routes").
			Field("upstream_key_id").
			Required().
			Unique().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (UpstreamKeyModelRoute) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("upstream_key_id", "public_model").Unique().
			Annotations(entsql.IndexWhere("deleted_at IS NULL")),
		index.Fields("upstream_key_id", "enabled"),
		index.Fields("upstream_key_id", "target_platform", "enabled"),
		index.Fields("status"),
	}
}
