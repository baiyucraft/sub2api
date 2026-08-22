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

// UpstreamAuthSession stores encrypted provider-neutral authentication state.
type UpstreamAuthSession struct {
	ent.Schema
}

func (UpstreamAuthSession) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "upstream_auth_sessions"}}
}

func (UpstreamAuthSession) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (UpstreamAuthSession) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("upstream_config_id"),
		field.String("provider").MaxLen(32).NotEmpty(),
		field.String("auth_mode").MaxLen(32).NotEmpty(),
		field.String("credential_fingerprint").MaxLen(64).NotEmpty(),
		field.String("secret_ciphertext").SchemaType(map[string]string{dialect.Postgres: "text"}).Sensitive(),
		field.Time("expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_authenticated_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_refreshed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_used_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("cooldown_until").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("consecutive_auth_failures").Default(0),
		field.String("last_error_category").MaxLen(64).Optional().Nillable(),
		field.Time("last_error_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int64("login_count").Default(0),
		field.Int64("reuse_count").Default(0),
		field.Int64("refresh_count").Default(0),
		field.Int64("relogin_count").Default(0),
		field.Int64("cooldown_count").Default(0),
	}
}

func (UpstreamAuthSession) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("upstream_config", UpstreamConfig.Type).
			Ref("auth_session").
			Field("upstream_config_id").
			Required().
			Unique(),
	}
}

func (UpstreamAuthSession) Indexes() []ent.Index {
	return []ent.Index{index.Fields("upstream_config_id").Unique()}
}
