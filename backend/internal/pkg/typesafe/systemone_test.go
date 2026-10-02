package typesafe

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSystemOneRequestValidQuestionTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"noul string state", `{"model":"jev-latest","state":"sample","questions":{"safety":{"type":"noul","instructions":"Evaluate safety","criteria":{"safe":"No harm"}}}}`},
		{"choice object state", `{"model":"jev-latest","state":{"text":"sample"},"questions":{"label":{"type":"choice","instructions":{"task":"Classify"},"criteria":{"safe":"Allowed","unsafe":null}}},"stream":false}`},
		{"score array state", `{"model":"jev-latest","state":["sample"],"questions":{"quality":{"type":"score","instructions":["Rate quality"],"criteria":["poor","good"]}}}`},
		{"noul omitted instructions", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"noul"}}}`},
		{"noul nullable instructions and criteria", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"noul","instructions":null,"criteria":null}}}`},
		{"noul structured descriptions", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"noul","criteria":{"true":{"examples":["yes",null]},"false":["no",null],"extension":42}}}}`},
		{"choice structured descriptions", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"choice","instructions":null,"criteria":{"a":{"description":"A","extra":null},"b":["B",null],"c":null}}}}`},
		{"choice empty criteria", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"choice","criteria":{}}}}`},
		{"score one level", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"score","criteria":["only"]}}}`},
		{"score object level", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"score","instructions":null,"criteria":[{"description":"only","extra":null}]}}}`},
		{"score array level", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"score","criteria":[["only",null]]}}}`},
		{"native extensions preserved", `{"model":"jev-latest","state":"sample","questions":{"q":{"type":"noul","extension":{"kept":true}}},"extension":[1,null]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, err := ValidateSystemOneRequest([]byte(tc.body))
			require.NoError(t, err)
			require.Equal(t, JevLatestModel, model)
		})
	}
}

func TestValidateSystemOneRequestRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"invalid json", `{`, "invalid JSON"},
		{"missing model", `{"state":"x","questions":{"q":{"type":"noul","instructions":"x"}}}`, "model"},
		{"illegal model", `{"model":"jev-old","state":"x","questions":{"q":{"type":"noul","instructions":"x"}}}`, "jev-latest"},
		{"model with whitespace", `{"model":" jev-latest ","state":"x","questions":{"q":{"type":"noul","instructions":"x"}}}`, "jev-latest"},
		{"missing state", `{"model":"jev-latest","questions":{"q":{"type":"noul","instructions":"x"}}}`, "state"},
		{"scalar state", `{"model":"jev-latest","state":42,"questions":{"q":{"type":"noul","instructions":"x"}}}`, "state"},
		{"empty questions", `{"model":"jev-latest","state":"x","questions":{}}`, "questions"},
		{"array questions", `{"model":"jev-latest","state":"x","questions":[{"type":"noul"}]}`, "questions must be a non-empty object"},
		{"null questions", `{"model":"jev-latest","state":"x","questions":null}`, "questions must be a non-empty object"},
		{"unknown question type", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"boolean","instructions":"x"}}}`, "unsupported type"},
		{"question type with whitespace", `{"model":"jev-latest","state":"x","questions":{"q":{"type":" noul ","instructions":"x"}}}`, "unsupported type"},
		{"noul criteria array", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul","instructions":"x","criteria":[]}}}`, "noul criteria"},
		{"noul numeric description", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul","criteria":{"true":1}}}}`, "noul criteria"},
		{"noul boolean description", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul","criteria":{"false":false}}}}`, "noul criteria"},
		{"missing choice criteria", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"choice"}}}`, "choice criteria"},
		{"null choice criteria", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"choice","criteria":null}}}`, "choice criteria"},
		{"choice criteria array", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"choice","criteria":[]}}}`, "choice criteria"},
		{"choice numeric value", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"choice","instructions":"x","criteria":{"one":1}}}}`, "choice criteria"},
		{"missing score criteria", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"score"}}}`, "score criteria"},
		{"empty score criteria", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"score","criteria":[]}}}`, "score criteria"},
		{"null score criteria", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"score","criteria":null}}}`, "score criteria"},
		{"score map is not wire protocol", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"score","criteria":{"0":"low"}}}}`, "score criteria"},
		{"numeric score level", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"score","criteria":["low",2]}}}`, "score criteria"},
		{"null score level", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"score","criteria":[null]}}}`, "score criteria"},
		{"numeric instructions", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul","instructions":1}}}`, "instructions"},
		{"boolean instructions", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"choice","instructions":true,"criteria":{}}}}`, "instructions"},
		{"stream true", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul","instructions":"x"}},"stream":true}`, "streaming"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateSystemOneRequest([]byte(tc.body))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			if tc.name == "stream true" {
				require.True(t, errors.Is(err, ErrStreamingUnsupported))
			}
		})
	}
}
