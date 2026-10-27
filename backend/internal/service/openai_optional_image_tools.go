package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func isOptionalOpenAIImageTool(tool gjson.Result) bool {
	return isOpenAIImageGenerationType(tool.Get("type").String()) || isPassiveOpenAIImageTool(tool)
}

// A required selection is image work only when every permitted tool is an image
// tool. An allowed_tools restriction takes precedence over the full catalog.
func openAIRequiredChoiceOnlyImages(root gjson.Result) bool {
	choice := root.Get("tool_choice")
	var tools []gjson.Result
	if choice.Get("type").String() == "allowed_tools" {
		if choice.Get("mode").String() != "required" {
			return false
		}
		tools = choice.Get("tools").Array()
	} else {
		if choice.String() != "required" {
			return false
		}
		tools = append(tools, root.Get("tools").Array()...)
		for _, item := range root.Get("input").Array() {
			if item.Get("type").String() == "additional_tools" {
				tools = append(tools, item.Get("tools").Array()...)
			}
		}
	}
	if len(tools) == 0 {
		return false
	}
	for _, tool := range tools {
		if !isOptionalOpenAIImageTool(tool) && !openAIJSONToolChoiceSelectsExplicitImageGeneration(tool) {
			return false
		}
	}
	return true
}

// Resolve output references in two passes so item order never changes intent.
// Unknown IDs are diagnostic uncertainty, not evidence of an image invocation.
func describeOpenAIImageHistory(root gjson.Result) (history, unknown bool) {
	known := make(map[string]bool)
	var outputs []string
	var walk func(gjson.Result, bool)
	walk = func(v gjson.Result, inToolCalls bool) {
		if v.IsArray() {
			v.ForEach(func(_, item gjson.Result) bool { walk(item, inToolCalls); return true })
			return
		}
		if !v.IsObject() || v.Get("type").String() == "additional_tools" {
			return
		}
		typ := v.Get("type").String()
		image := isOpenAIImageGenFunctionReference(v.Get("namespace").String(), v.Get("name").String()) || isOpenAIImageGenFunctionReference(v.Get("function.namespace").String(), v.Get("function.name").String())
		output := isCodexToolCallOutputItemType(typ) || typ == "tool_result" || typ == "tool" || v.Get("role").String() == "tool"
		call := isCodexToolCallContextItemType(typ) || typ == "image_generation_call" || typ == "tool_use" || inToolCalls
		id := strings.TrimSpace(v.Get("call_id").String())
		if output {
			if id == "" {
				id = strings.TrimSpace(v.Get("tool_call_id").String())
			}
			if id == "" {
				id = strings.TrimSpace(v.Get("tool_use_id").String())
			}
			history = history || image
			outputs = append(outputs, id)
			return // Tool output content is data, never executable call history.
		} else if call {
			image = image || typ == "image_generation_call"
			history = history || image
			if id == "" {
				id = strings.TrimSpace(v.Get("id").String())
			}
			// An unnamed context cannot resolve what an output belongs to.
			if id != "" && (image || v.Get("name").String() != "" || v.Get("function.name").String() != "" || typ == "local_shell_call") {
				known[id] = known[id] || image
			}
		}
		v.ForEach(func(key, child gjson.Result) bool {
			switch key.String() {
			case "tools", "arguments", "parameters", "output", "result", "function":
				return true
			}
			walk(child, key.String() == "tool_calls")
			return true
		})
	}
	walk(root.Get("input"), false)
	walk(root.Get("messages"), false)
	for _, id := range outputs {
		if image, ok := known[id]; ok && id != "" {
			history = history || image
		} else {
			unknown = true
		}
	}
	return history, unknown
}

func openAIImageContextStripsOptional(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	p, _ := ctx.Value(openAIImagePolicyContextKey{}).(openAIImageRequestPolicy)
	return p.stripOptional || (p.groupAllowed != nil && !*p.groupAllowed)
}

// ReconcileOpenAIImageRequestDescriptor carries only proven hard intent through
// transforms. Removed optional declarations and unresolved history stay optional.
func ReconcileOpenAIImageRequestDescriptor(final, original OpenAIImageRequest) OpenAIImageRequest {
	final.NativeRequired = final.Native && original.NativeRequired
	final.Explicit = final.Explicit || original.Explicit
	final.History = final.History || original.History
	final.UnknownHistory = final.UnknownHistory || original.UnknownHistory
	final.PreviousResponse = final.PreviousResponse || original.PreviousResponse
	final.IsCodex = final.IsCodex || original.IsCodex
	final.Lite = final.Lite || original.Lite
	if final.Explicit {
		final.Source = "explicit"
	} else if final.History {
		final.Source = "history"
	}
	return final
}

func ApplyOpenAIImageRequestGroupPolicy(body []byte, r OpenAIImageRequest, group *Group) ([]byte, OpenAIImageRequest, bool, error) {
	ctx := WithOpenAIImageRequestGroup(context.Background(), group)
	return ApplyOpenAIImageRequestContextPolicy(ctx, body, r)
}

func ApplyOpenAIImageRequestContextPolicy(ctx context.Context, body []byte, r OpenAIImageRequest) ([]byte, OpenAIImageRequest, bool, error) {
	if !openAIImageContextStripsOptional(ctx) {
		// Retain the existing native declaration contract when cleanup is off.
		r.NativeRequired = r.Native
		return body, r, false, nil
	}
	r.NativeRequired = false
	if !r.Valid {
		// Ambiguous JSON cannot be classified or rewritten safely: different
		// parsers may select different duplicate keys and hide an image catalog.
		return body, r, false, fmt.Errorf("invalid image tool declarations")
	}
	next, changed, err := stripOptionalOpenAIImageTools(body, r)
	if err != nil || !changed {
		return body, r, false, err
	}
	effective := DescribeOpenAIImageRequest(r.Endpoint, r.Model, next, r.IsCodex)
	return next, ReconcileOpenAIImageRequestDescriptor(effective, r), true, nil
}

// stripOptionalOpenAIImageTools leaves calls, outputs, prompts and continuation
// IDs byte-for-byte intact; only catalogs and their choice restrictions change.
func stripOptionalOpenAIImageTools(body []byte, r OpenAIImageRequest) ([]byte, bool, error) {
	if !r.CanStripPassive() {
		// A continuation may restrict inherited tools without repeating their
		// catalog. Optional image references must still be removed from that
		// restriction, without changing unrelated empty restrictions.
		choice := gjson.GetBytes(body, "tool_choice")
		hasImageReference := false
		if choice.Get("type").String() == "allowed_tools" {
			for _, tool := range choice.Get("tools").Array() {
				hasImageReference = hasImageReference || isOptionalOpenAIImageTool(tool) || openAIJSONToolChoiceSelectsExplicitImageGeneration(tool)
			}
		}
		if !r.Valid || r.RequiresCapability() || !hasImageReference {
			return body, false, nil
		}
	}
	next, changed := body, false
	strip := func(path string, choices bool) error {
		for i := len(gjson.GetBytes(next, path).Array()) - 1; i >= 0; i-- {
			tool := gjson.GetBytes(next, fmt.Sprintf("%s.%d", path, i))
			if !isOptionalOpenAIImageTool(tool) && !(choices && openAIJSONToolChoiceSelectsExplicitImageGeneration(tool)) {
				continue
			}
			var err error
			next, err = sjson.DeleteBytes(next, fmt.Sprintf("%s.%d", path, i))
			if err != nil {
				return err
			}
			changed = true
		}
		return nil
	}
	if err := strip("tools", false); err != nil {
		return body, false, err
	}
	items := gjson.GetBytes(next, "input").Array()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Get("type").String() != "additional_tools" {
			continue
		}
		path := fmt.Sprintf("input.%d.tools", i)
		before := len(gjson.GetBytes(next, path).Array())
		if err := strip(path, false); err != nil {
			return body, false, err
		}
		if before > 0 && len(gjson.GetBytes(next, path).Array()) == 0 {
			var err error
			next, err = sjson.DeleteBytes(next, fmt.Sprintf("input.%d", i))
			if err != nil {
				return body, false, err
			}
		}
	}
	choice := gjson.GetBytes(next, "tool_choice")
	if choice.Get("type").String() == "allowed_tools" {
		if err := strip("tool_choice.tools", true); err != nil {
			return body, false, err
		}
		if len(gjson.GetBytes(next, "tool_choice.tools").Array()) == 0 {
			// Removing a restriction would enable previously forbidden tools.
			var err error
			next, err = sjson.SetBytes(next, "tool_choice", "none")
			if err != nil {
				return body, false, err
			}
			changed = true
		}
	}
	return next, changed, nil
}
