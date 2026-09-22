package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	PluginAdminUISchemaVersion      = 1
	PluginAdminUIDefinitionMaxBytes = 1 << 20
	PluginAdminUIMaxNodes           = 512
	PluginAdminUIMaxDepth           = 12
	PluginAdminUIMaxTextLength      = 1024
	PluginAdminUIMaxIDLength        = 128
	PluginAdminUIMaxOptions         = 256
)

var nativeAdminUINodeTypes = map[string]bool{
	"stack": true, "grid": true, "section": true, "tabs": true,
	"accordion": true, "text": true, "alert": true, "badge": true,
	"stat": true, "key_value": true, "table": true, "log_list": true,
	"empty": true, "text_input": true, "number_input": true,
	"switch": true, "select": true, "multiselect": true, "search": true,
	"checkbox": true, "repeat": true, "matrix": true, "button": true,
	"action_group": true,
}

var nativeAdminUIIcons = map[string]bool{
	"play": true, "refresh": true, "edit": true, "trash": true, "plus": true,
	"search": true, "more": true, "chart": true, "clock": true, "link": true,
	"sync": true, "check": true, "x": true, "eye": true, "eyeOff": true,
	"cog": true, "grid": true, "chat": true, "lightbulb": true, "arrowRight": true,
	"arrowLeft": true, "arrowUp": true, "arrowDown": true, "checkCircle": true,
	"xCircle": true, "exclamationCircle": true, "exclamationTriangle": true,
	"trophy": true, "star": true, "infoCircle": true, "questionCircle": true,
	"user": true, "userCircle": true, "userPlus": true, "users": true,
	"document": true, "clipboard": true, "copy": true, "inbox": true,
	"download": true, "upload": true, "filter": true, "globe": true, "sort": true,
	"key": true, "lock": true, "shield": true, "menu": true, "calendar": true,
	"home": true, "terminal": true, "gift": true, "creditCard": true, "mail": true,
	"chartBar": true, "trendingUp": true, "database": true, "cube": true,
	"bell": true, "bolt": true, "sparkles": true, "cloud": true, "server": true,
	"sun": true, "moon": true, "book": true, "dollar": true, "ban": true,
	"login": true, "swap": true, "beaker": true, "cpu": true, "chatBubble": true,
	"calculator": true, "fire": true, "badge": true, "brain": true,
}

// NativePluginAdminUI is the normalized, single-page declaration returned by
// the admin-ui endpoint. It contains no executable plugin content.
type NativePluginAdminUI struct {
	SchemaVersion       int                  `json:"schema_version"`
	Title               string               `json:"title"`
	Description         string               `json:"description"`
	PollIntervalSeconds int                  `json:"poll_interval_seconds"`
	Layout              []NativePluginUINode `json:"layout"`
}

type NativePluginUINode struct {
	Type        string                      `json:"type"`
	ID          string                      `json:"id,omitempty"`
	Icon        string                      `json:"icon,omitempty"`
	Title       string                      `json:"title,omitempty"`
	Description string                      `json:"description,omitempty"`
	Bind        string                      `json:"bind,omitempty"`
	Write       string                      `json:"write,omitempty"`
	Condition   *NativePluginUICondition    `json:"condition,omitempty"`
	Children    []NativePluginUINode        `json:"children,omitempty"`
	Options     []NativePluginUIOption      `json:"options,omitempty"`
	Columns     []NativePluginUITableColumn `json:"columns,omitempty"`
	Action      string                      `json:"action,omitempty"`
	Payload     map[string]string           `json:"payload,omitempty"`
	Confirm     bool                        `json:"confirm,omitempty"`
	ReadOnly    bool                        `json:"read_only,omitempty"`
}

type NativePluginUIOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type NativePluginUITableColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Bind  string `json:"bind,omitempty"`
}

type NativePluginUICondition struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func ParseNativePluginAdminUI(raw []byte, manifest PluginManifest) (*NativePluginAdminUI, error) {
	if len(raw) == 0 || len(raw) > PluginAdminUIDefinitionMaxBytes {
		return nil, errors.New("原生插件页面定义超过大小限制")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var definition NativePluginAdminUI
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("解析原生插件页面定义: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("原生插件页面定义只能包含一个 JSON 对象")
	}
	if definition.SchemaVersion != PluginAdminUISchemaVersion {
		return nil, fmt.Errorf("不支持的原生插件页面定义版本: %d", definition.SchemaVersion)
	}
	if err := validateAdminUIText(definition.Title, true); err != nil {
		return nil, fmt.Errorf("原生插件页面标题无效: %w", err)
	}
	if err := validateAdminUIText(definition.Description, false); err != nil {
		return nil, fmt.Errorf("原生插件页面描述无效: %w", err)
	}
	if definition.PollIntervalSeconds < 2 || definition.PollIntervalSeconds > 60 {
		return nil, errors.New("原生插件页面轮询间隔必须为 2 到 60 秒")
	}
	if len(definition.Layout) == 0 {
		return nil, errors.New("原生插件页面 layout 不能为空")
	}
	count := 0
	seenIDs := make(map[string]struct{})
	for i := range definition.Layout {
		if err := validateAdminUINode(&definition.Layout[i], &count, 1, manifest, seenIDs); err != nil {
			return nil, fmt.Errorf("原生插件页面 layout[%d] 无效: %w", i, err)
		}
	}
	return &definition, nil
}

func validateAdminUINode(node *NativePluginUINode, count *int, depth int, manifest PluginManifest, seenIDs map[string]struct{}) error {
	(*count)++
	if *count > PluginAdminUIMaxNodes {
		return errors.New("原生插件页面节点数量超过限制")
	}
	if depth > PluginAdminUIMaxDepth {
		return errors.New("原生插件页面嵌套深度超过限制")
	}
	if !nativeAdminUINodeTypes[node.Type] {
		return fmt.Errorf("不支持的节点类型 %q", node.Type)
	}
	if node.ID != "" {
		if len(node.ID) > PluginAdminUIMaxIDLength || !isSafeAdminUIIdentifier(node.ID) {
			return errors.New("节点 id 无效")
		}
		if _, exists := seenIDs[node.ID]; exists {
			return fmt.Errorf("节点 id 重复: %s", node.ID)
		}
		seenIDs[node.ID] = struct{}{}
	}
	if node.Icon != "" && !nativeAdminUIIcons[node.Icon] {
		return fmt.Errorf("不支持的图标 %q", node.Icon)
	}
	if err := validateAdminUIText(node.Title, false); err != nil {
		return fmt.Errorf("节点标题无效: %w", err)
	}
	if err := validateAdminUIText(node.Description, false); err != nil {
		return fmt.Errorf("节点描述无效: %w", err)
	}
	if node.Bind != "" {
		if err := validateAdminUIBinding(node.Bind, false, manifest); err != nil {
			return fmt.Errorf("bind 无效: %w", err)
		}
	}
	if node.Write != "" {
		if !isWritableAdminUINode(node.Type) {
			return errors.New("只有输入节点允许 write 绑定")
		}
		if err := validateAdminUIBinding(node.Write, true, manifest); err != nil {
			return fmt.Errorf("write 无效: %w", err)
		}
	}
	if node.Condition != nil {
		if err := validateAdminUICondition(node.Condition, manifest); err != nil {
			return fmt.Errorf("condition 无效: %w", err)
		}
	}
	if node.Action != "" {
		if !isSafeAdminUIIdentifier(node.Action) {
			return errors.New("action 名称无效")
		}
	}
	for key, path := range node.Payload {
		if len(key) > PluginAdminUIMaxIDLength || !isSafeAdminUIIdentifier(key) {
			return errors.New("action payload 键名无效")
		}
		if err := validateAdminUIBinding(path, false, manifest); err != nil {
			return fmt.Errorf("action payload 绑定无效: %w", err)
		}
	}
	if len(node.Options) > PluginAdminUIMaxOptions {
		return errors.New("选项数量超过限制")
	}
	for _, option := range node.Options {
		if err := validateAdminUIText(option.Value, true); err != nil {
			return fmt.Errorf("选项 value 无效: %w", err)
		}
		if err := validateAdminUIText(option.Label, true); err != nil {
			return fmt.Errorf("选项 label 无效: %w", err)
		}
	}
	for _, column := range node.Columns {
		if !isSafeAdminUIIdentifier(column.Key) {
			return errors.New("表格列 key 无效")
		}
		if err := validateAdminUIText(column.Label, true); err != nil {
			return fmt.Errorf("表格列 label 无效: %w", err)
		}
		if column.Bind != "" {
			if err := validateAdminUIBinding(column.Bind, false, manifest); err != nil {
				return fmt.Errorf("表格列 bind 无效: %w", err)
			}
		}
	}
	for i := range node.Children {
		if err := validateAdminUINode(&node.Children[i], count, depth+1, manifest, seenIDs); err != nil {
			return err
		}
	}
	return nil
}

func validateAdminUICondition(condition *NativePluginUICondition, manifest PluginManifest) error {
	switch condition.Op {
	case "eq", "ne", "truthy", "falsy", "in":
	default:
		return fmt.Errorf("不支持的条件操作符 %q", condition.Op)
	}
	if err := validateAdminUIBinding(condition.Path, false, manifest); err != nil {
		return err
	}
	if (condition.Op == "truthy" || condition.Op == "falsy") && condition.Value != nil {
		return errors.New("truthy/falsy 不能包含 value")
	}
	if condition.Op == "in" {
		values, ok := condition.Value.([]any)
		if !ok || len(values) == 0 || len(values) > PluginAdminUIMaxOptions {
			return errors.New("in 条件 value 必须是有限数组")
		}
	}
	return nil
}

func validateAdminUIBinding(path string, writable bool, manifest PluginManifest) error {
	segments, err := parseAdminUIJSONPointer(path)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return errors.New("绑定路径不能指向根")
	}
	switch segments[0] {
	case "config", "resources", "status", "local", "item":
	default:
		return errors.New("绑定根必须是 config、resources、status、local 或 item")
	}
	if writable && segments[0] != "config" {
		return errors.New("可写绑定只能指向 config")
	}
	if writable && len(segments) == 1 {
		return errors.New("可写绑定不能直接覆盖 config 根对象")
	}
	for _, segment := range segments {
		if segment == "__proto__" || segment == "prototype" || segment == "constructor" {
			return errors.New("绑定路径包含禁止的原型污染段")
		}
		if segment == "_host_secrets" {
			return errors.New("绑定路径不能访问宿主秘密")
		}
	}
	if segments[0] == "config" {
		for _, secret := range manifest.ConfigSecrets {
			for _, segment := range segments[1:] {
				if segment == secret {
					return errors.New("绑定路径不能访问插件秘密字段")
				}
			}
		}
	}
	return nil
}

func parseAdminUIJSONPointer(path string) ([]string, error) {
	if path == "" || path[0] != '/' || strings.ContainsRune(path, '\x00') {
		return nil, errors.New("必须是 JSON Pointer")
	}
	rawSegments := strings.Split(path[1:], "/")
	segments := make([]string, len(rawSegments))
	for i, raw := range rawSegments {
		var decoded strings.Builder
		for offset := 0; offset < len(raw); offset++ {
			if raw[offset] != '~' {
				decoded.WriteByte(raw[offset])
				continue
			}
			if offset+1 >= len(raw) || (raw[offset+1] != '0' && raw[offset+1] != '1') {
				return nil, errors.New("JSON Pointer 转义无效")
			}
			if raw[offset+1] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
			offset++
		}
		segments[i] = decoded.String()
	}
	return segments, nil
}

func validateAdminUIText(value string, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return errors.New("不能为空")
	}
	if len(value) > PluginAdminUIMaxTextLength {
		return errors.New("文本长度超过限制")
	}
	return nil
}

func isSafeAdminUIIdentifier(value string) bool {
	if value == "" || len(value) > PluginAdminUIMaxIDLength {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || (r == '.' && i > 0) {
			continue
		}
		return false
	}
	return true
}

func isWritableAdminUINode(nodeType string) bool {
	switch nodeType {
	case "text_input", "number_input", "switch", "select", "multiselect", "search", "checkbox":
		return true
	default:
		return false
	}
}
