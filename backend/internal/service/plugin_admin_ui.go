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
	PluginAdminUIMaxColumns         = 64
	PluginAdminUIMaxRowActions      = 16
	PluginAdminUIMaxActionFields    = 64
	PluginAdminUIMaxValidations     = 32
	PluginAdminUIMaxLiteralDepth    = 6
	PluginAdminUIMaxLiteralNodes    = 1024
	PluginAdminUIMaxLiteralBytes    = 64 << 10
)

var nativeAdminUINodeTypes = map[string]bool{
	"stack": true, "grid": true, "section": true, "tabs": true,
	"accordion": true, "text": true, "alert": true, "badge": true,
	"stat": true, "key_value": true, "table": true, "log_list": true,
	"empty": true, "text_input": true, "number_input": true,
	"switch": true, "select": true, "multiselect": true, "search": true,
	"checkbox": true, "repeat": true, "matrix": true, "data_table": true, "button": true,
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
	SchemaVersion       int                          `json:"schema_version"`
	Title               string                       `json:"title"`
	Description         string                       `json:"description"`
	TitleKey            string                       `json:"title_key,omitempty"`
	DescriptionKey      string                       `json:"description_key,omitempty"`
	Translations        map[string]map[string]string `json:"translations,omitempty"`
	PollIntervalSeconds int                          `json:"poll_interval_seconds"`
	Layout              []NativePluginUINode         `json:"layout"`
}

type NativePluginUINode struct {
	Type               string                      `json:"type"`
	ID                 string                      `json:"id,omitempty"`
	Icon               string                      `json:"icon,omitempty"`
	Title              string                      `json:"title,omitempty"`
	Description        string                      `json:"description,omitempty"`
	TitleKey           string                      `json:"title_key,omitempty"`
	DescriptionKey     string                      `json:"description_key,omitempty"`
	Bind               string                      `json:"bind,omitempty"`
	Format             string                      `json:"format,omitempty"`
	Write              string                      `json:"write,omitempty"`
	Condition          *NativePluginUICondition    `json:"condition,omitempty"`
	Children           []NativePluginUINode        `json:"children,omitempty"`
	Options            []NativePluginUIOption      `json:"options,omitempty"`
	OptionsBind        string                      `json:"options_bind,omitempty"`
	Columns            []NativePluginUITableColumn `json:"columns,omitempty"`
	RowActions         []NativePluginUINode        `json:"row_actions,omitempty"`
	SelectionBind      string                      `json:"selection_bind,omitempty"`
	SearchBind         string                      `json:"search_bind,omitempty"`
	FilterBind         string                      `json:"filter_bind,omitempty"`
	FilterKey          string                      `json:"filter_key,omitempty"`
	FilterOptionsBind  string                      `json:"filter_options_bind,omitempty"`
	Filters            []NativePluginUITableFilter `json:"filters,omitempty"`
	PresenceFilterBind string                      `json:"presence_filter_bind,omitempty"`
	PresenceKey        string                      `json:"presence_key,omitempty"`
	RowKey             string                      `json:"row_key,omitempty"`
	PageSize           int                         `json:"page_size,omitempty"`
	SortKey            string                      `json:"sort_key,omitempty"`
	SortDesc           bool                        `json:"sort_desc,omitempty"`
	Action             string                      `json:"action,omitempty"`
	Payload            map[string]string           `json:"payload,omitempty"`
	Values             map[string]any              `json:"values,omitempty"`
	ApplyResult        string                      `json:"apply_result,omitempty"`
	Validation         []NativePluginUIValidation  `json:"validation,omitempty"`
	Confirm            bool                        `json:"confirm,omitempty"`
	ReadOnly           bool                        `json:"read_only,omitempty"`
}

type NativePluginUIValidation struct {
	Op      string `json:"op"`
	Value   any    `json:"value,omitempty"`
	Message string `json:"message"`
}

type NativePluginUIOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	LabelKey string `json:"label_key,omitempty"`
}

type NativePluginUITableColumn struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	LabelKey string `json:"label_key,omitempty"`
	Bind     string `json:"bind,omitempty"`
	Format   string `json:"format,omitempty"`
}

type NativePluginUITableFilter struct {
	Bind     string `json:"bind"`
	Key      string `json:"key"`
	AllValue string `json:"all_value,omitempty"`
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
	if err := validateAdminUITranslationKey(definition.TitleKey); err != nil {
		return nil, fmt.Errorf("原生插件页面 title_key 无效: %w", err)
	}
	if err := validateAdminUITranslationKey(definition.DescriptionKey); err != nil {
		return nil, fmt.Errorf("原生插件页面 description_key 无效: %w", err)
	}
	if err := validateAdminUITranslations(definition.Translations); err != nil {
		return nil, err
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

func validateAdminUINodeFieldMatrix(node *NativePluginUINode) error {
	if node == nil {
		return errors.New("节点不能为空")
	}
	allowsChildren := map[string]bool{
		"stack": true, "grid": true, "section": true, "tabs": true,
		"accordion": true, "repeat": true, "matrix": true, "data_table": true,
		"action_group": true,
	}
	allowsBind := map[string]bool{
		"text": true, "alert": true, "badge": true, "stat": true,
		"key_value": true, "table": true, "log_list": true, "empty": true,
		"text_input": true, "number_input": true, "switch": true,
		"select": true, "multiselect": true, "search": true, "checkbox": true,
		"repeat": true, "matrix": true, "data_table": true,
	}
	if len(node.Children) > 0 && !allowsChildren[node.Type] {
		return fmt.Errorf("%s 节点不允许 children", node.Type)
	}
	if node.Bind != "" && !allowsBind[node.Type] {
		return fmt.Errorf("%s 节点不允许 bind", node.Type)
	}
	if node.Format != "" && node.Type != "stat" && node.Type != "table" && node.Type != "log_list" && node.Type != "data_table" {
		return fmt.Errorf("%s 节点不允许 format", node.Type)
	}
	if (len(node.Options) > 0 || node.OptionsBind != "") && node.Type != "select" && node.Type != "multiselect" {
		return fmt.Errorf("%s 节点不允许 options", node.Type)
	}
	if (len(node.Columns) > 0) && node.Type != "table" && node.Type != "log_list" && node.Type != "data_table" {
		return fmt.Errorf("%s 节点不允许 columns", node.Type)
	}
	if node.Write != "" && !isWritableAdminUINode(node.Type) {
		return fmt.Errorf("%s 节点不允许 write", node.Type)
	}
	if (len(node.Validation) > 0 || node.ReadOnly) && !isWritableAdminUINode(node.Type) {
		return fmt.Errorf("%s 节点不允许 validation 或 read_only", node.Type)
	}
	if node.Action != "" || len(node.Payload) > 0 || len(node.Values) > 0 || node.ApplyResult != "" || node.Confirm {
		if node.Type != "button" {
			return fmt.Errorf("%s 节点不允许 Action 字段", node.Type)
		}
	}
	if node.Type == "button" && node.Action == "" {
		return errors.New("button 必须声明 action")
	}
	if node.Type != "data_table" && (node.SelectionBind != "" || node.SearchBind != "" || node.FilterBind != "" || node.FilterOptionsBind != "" || len(node.Filters) > 0 || node.PresenceFilterBind != "" || node.PresenceKey != "" || node.RowKey != "" || node.PageSize != 0 || node.SortKey != "" || node.SortDesc || len(node.RowActions) > 0) {
		return errors.New("表格专用字段只能用于 data_table")
	}
	if (node.Type == "table" || node.Type == "log_list") && (node.Bind == "" || len(node.Columns) == 0) {
		return fmt.Errorf("%s 必须声明 bind 和 columns", node.Type)
	}
	if (node.Type == "repeat" || node.Type == "matrix") && (node.Bind == "" || len(node.Children) == 0) {
		return fmt.Errorf("%s 必须声明 bind 和 children", node.Type)
	}
	if (node.Type == "tabs" || node.Type == "accordion" || node.Type == "action_group") && len(node.Children) == 0 {
		return fmt.Errorf("%s 必须声明 children", node.Type)
	}
	if node.Type == "data_table" && (node.Bind == "" || node.RowKey == "" || len(node.Columns) == 0) {
		return errors.New("data_table 必须声明 bind、row_key 和 columns")
	}
	if node.ApplyResult != "" && (node.ApplyResult != "config" || !node.Confirm) {
		return errors.New("apply_result=config 只允许需要确认的 Action button")
	}
	return nil
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
	if err := validateAdminUITranslationKey(node.TitleKey); err != nil {
		return fmt.Errorf("节点 title_key 无效: %w", err)
	}
	if err := validateAdminUITranslationKey(node.DescriptionKey); err != nil {
		return fmt.Errorf("节点 description_key 无效: %w", err)
	}
	if node.Bind != "" {
		if err := validateAdminUIBinding(node.Bind, false, manifest); err != nil {
			return fmt.Errorf("bind 无效: %w", err)
		}
	}
	if node.Format != "" {
		switch node.Format {
		case "count", "boolean", "date", "duration":
		default:
			return fmt.Errorf("不支持的格式化方式 %q", node.Format)
		}
	}
	if node.Write != "" {
		if !isWritableAdminUINode(node.Type) {
			return errors.New("只有输入节点允许 write 绑定")
		}
		if err := validateAdminUIBinding(node.Write, true, manifest); err != nil {
			return fmt.Errorf("write 无效: %w", err)
		}
		if node.Bind == "" {
			node.Bind = node.Write
		} else if node.Bind != node.Write {
			return errors.New("输入节点的 bind 和 write 必须指向同一路径")
		}
	}
	if node.SelectionBind != "" {
		if err := validateAdminUILocalBinding(node.SelectionBind, manifest); err != nil {
			return fmt.Errorf("selection_bind 无效: %w", err)
		}
	}
	if node.SearchBind != "" {
		if err := validateAdminUILocalBinding(node.SearchBind, manifest); err != nil {
			return fmt.Errorf("search_bind 无效: %w", err)
		}
	}
	if node.FilterBind != "" {
		if err := validateAdminUILocalBinding(node.FilterBind, manifest); err != nil {
			return fmt.Errorf("filter_bind 无效: %w", err)
		}
	}
	if node.FilterKey != "" && !isSafeAdminUIPropertyIdentifier(node.FilterKey) {
		return errors.New("filter_key 无效")
	}
	if node.FilterOptionsBind != "" {
		if err := validateAdminUIBinding(node.FilterOptionsBind, false, manifest); err != nil {
			return fmt.Errorf("filter_options_bind 无效: %w", err)
		}
	}
	if node.Type != "data_table" && (node.SelectionBind != "" || node.SearchBind != "" || node.FilterBind != "" || node.FilterOptionsBind != "" || len(node.Filters) > 0 || node.PresenceFilterBind != "" || node.PresenceKey != "" || node.RowKey != "" || node.PageSize != 0 || node.SortKey != "" || node.SortDesc || len(node.RowActions) > 0) {
		return errors.New("表格专用字段只能用于 data_table")
	}
	if len(node.Filters) > 8 {
		return errors.New("表格筛选数量超过限制")
	}
	for _, filter := range node.Filters {
		if err := validateAdminUILocalBinding(filter.Bind, manifest); err != nil {
			return fmt.Errorf("表格筛选 bind 无效: %w", err)
		}
		if !isSafeAdminUIPropertyIdentifier(filter.Key) {
			return errors.New("表格筛选 key 无效")
		}
		if err := validateAdminUIText(filter.AllValue, false); err != nil {
			return fmt.Errorf("表格筛选 all_value 无效: %w", err)
		}
	}
	if node.PresenceFilterBind != "" {
		if err := validateAdminUILocalBinding(node.PresenceFilterBind, manifest); err != nil {
			return fmt.Errorf("presence_filter_bind 无效: %w", err)
		}
		if !isSafeAdminUIPropertyIdentifier(node.PresenceKey) {
			return errors.New("presence filter 无效")
		}
	}
	if node.PageSize < 0 || node.PageSize > 100 {
		return errors.New("page_size 必须在 0 到 100 之间")
	}
	if node.RowKey != "" && !isSafeAdminUIPropertyIdentifier(node.RowKey) {
		return errors.New("row_key 无效")
	}
	if node.SortKey != "" && !isSafeAdminUIPropertyIdentifier(node.SortKey) {
		return errors.New("sort_key 无效")
	}
	if node.Type == "data_table" {
		if node.Bind == "" || node.RowKey == "" || len(node.Columns) == 0 {
			return errors.New("data_table 必须声明 bind、row_key 和 columns")
		}
		if len(node.Columns) > PluginAdminUIMaxColumns || len(node.RowActions) > PluginAdminUIMaxRowActions {
			return errors.New("data_table 列或行操作数量超过限制")
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
	if len(node.Payload) > PluginAdminUIMaxActionFields {
		return errors.New("action payload 字段数量超过限制")
	}
	if len(node.Values) > PluginAdminUIMaxActionFields {
		return errors.New("action values 字段数量超过限制")
	}
	for key, path := range node.Payload {
		if !isSafeAdminUIPropertyIdentifier(key) {
			return errors.New("action payload 键名无效")
		}
		if _, exists := node.Values[key]; exists {
			return fmt.Errorf("action 字段 %q 不能同时出现在 payload 和 values", key)
		}
		if err := validateAdminUIBinding(path, false, manifest); err != nil {
			return fmt.Errorf("action payload 绑定无效: %w", err)
		}
	}
	for key, value := range node.Values {
		if !isSafeAdminUIPropertyIdentifier(key) {
			return errors.New("action values 键名无效")
		}
		literalCount := 0
		if err := validateAdminUIFixedValue(value, 1, &literalCount); err != nil {
			return fmt.Errorf("action values 无效: %w", err)
		}
	}
	if raw, err := json.Marshal(node.Values); err != nil || len(raw) > PluginAdminUIMaxLiteralBytes {
		return errors.New("action values 超过限制")
	}
	if node.ApplyResult != "" {
		if node.ApplyResult != "config" || node.Type != "button" || node.Action == "" || !node.Confirm {
			return errors.New("apply_result=config 只允许需要确认的 Action button")
		}
	}
	if len(node.Validation) > PluginAdminUIMaxValidations {
		return errors.New("校验规则数量超过限制")
	}
	for _, rule := range node.Validation {
		if rule.Op != "required" && rule.Op != "min_length" && rule.Op != "max_length" && rule.Op != "in" {
			return fmt.Errorf("不支持的校验操作 %q", rule.Op)
		}
		if err := validateAdminUIText(rule.Message, true); err != nil {
			return fmt.Errorf("校验提示无效: %w", err)
		}
		switch rule.Op {
		case "required":
			if rule.Value != nil {
				return errors.New("required 校验不能包含 value")
			}
		case "min_length", "max_length":
			value, ok := rule.Value.(float64)
			if !ok || value < 0 || value > 4096 || value != float64(int(value)) {
				return errors.New("长度校验 value 必须是 0 到 4096 的整数")
			}
		case "in":
			values, ok := rule.Value.([]any)
			if !ok || len(values) == 0 || len(values) > PluginAdminUIMaxOptions {
				return errors.New("in 校验 value 必须是有限数组")
			}
			for _, value := range values {
				if err := validateAdminUIScalar(value); err != nil {
					return fmt.Errorf("in 校验 value 无效: %w", err)
				}
			}
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
		if err := validateAdminUITranslationKey(option.LabelKey); err != nil {
			return fmt.Errorf("选项 label_key 无效: %w", err)
		}
	}
	if node.OptionsBind != "" {
		if err := validateAdminUIBinding(node.OptionsBind, false, manifest); err != nil {
			return fmt.Errorf("options_bind 无效: %w", err)
		}
	}
	seenColumns := make(map[string]struct{}, len(node.Columns))
	for _, column := range node.Columns {
		if !isSafeAdminUIPropertyIdentifier(column.Key) {
			return errors.New("表格列 key 无效")
		}
		if _, exists := seenColumns[column.Key]; exists {
			return fmt.Errorf("表格列 key 重复: %s", column.Key)
		}
		seenColumns[column.Key] = struct{}{}
		if err := validateAdminUIText(column.Label, true); err != nil {
			return fmt.Errorf("表格列 label 无效: %w", err)
		}
		if err := validateAdminUITranslationKey(column.LabelKey); err != nil {
			return fmt.Errorf("表格列 label_key 无效: %w", err)
		}
		if column.Format != "" && column.Format != "count" && column.Format != "boolean" && column.Format != "date" && column.Format != "duration" {
			return fmt.Errorf("表格列 format 无效: %s", column.Format)
		}
		if column.Bind != "" {
			if err := validateAdminUIBinding(column.Bind, false, manifest); err != nil {
				return fmt.Errorf("表格列 bind 无效: %w", err)
			}
			if err := requireAdminUIBindingRoot(column.Bind, "item"); err != nil {
				return fmt.Errorf("表格列 bind 无效: %w", err)
			}
		}
	}
	for i := range node.Children {
		if err := validateAdminUINode(&node.Children[i], count, depth+1, manifest, seenIDs); err != nil {
			return err
		}
	}
	for i := range node.RowActions {
		if node.Type != "data_table" || node.RowActions[i].Type != "button" {
			return errors.New("row_actions 只允许 data_table 中的 button")
		}
		if err := validateAdminUINode(&node.RowActions[i], count, depth+1, manifest, seenIDs); err != nil {
			return err
		}
	}
	return nil
}

func validateAdminUITranslationKey(value string) error {
	if value == "" {
		return nil
	}
	if !isSafeAdminUIIdentifier(value) {
		return errors.New("必须是安全标识符")
	}
	return nil
}

func validateAdminUITranslations(translations map[string]map[string]string) error {
	if len(translations) > 8 {
		return errors.New("原生插件页面语言数量超过限制")
	}
	for locale, entries := range translations {
		if !isSafeAdminUIIdentifier(locale) || len(entries) > PluginAdminUIMaxNodes*2 {
			return errors.New("原生插件页面翻译表无效")
		}
		for key, value := range entries {
			if err := validateAdminUITranslationKey(key); err != nil {
				return fmt.Errorf("翻译键无效: %w", err)
			}
			if err := validateAdminUIText(value, true); err != nil {
				return fmt.Errorf("翻译文本无效: %w", err)
			}
		}
	}
	return nil
}

func validateAdminUIFixedValue(value any, depth int, count *int) error {
	*count++
	if *count > PluginAdminUIMaxLiteralNodes {
		return errors.New("字面量节点数量超过限制")
	}
	if depth > PluginAdminUIMaxLiteralDepth {
		return errors.New("嵌套深度超过限制")
	}
	switch typed := value.(type) {
	case nil, bool, float64:
		return nil
	case string:
		return validateAdminUIText(typed, false)
	case []any:
		if len(typed) > PluginAdminUIMaxOptions {
			return errors.New("数组长度超过限制")
		}
		for _, item := range typed {
			if err := validateAdminUIFixedValue(item, depth+1, count); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if len(typed) > PluginAdminUIMaxOptions {
			return errors.New("对象字段超过限制")
		}
		for key, item := range typed {
			if !isSafeAdminUIPropertyIdentifier(key) {
				return errors.New("对象键名无效")
			}
			if err := validateAdminUIFixedValue(item, depth+1, count); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("包含不支持的值类型")
	}
}

func validateAdminUIScalar(value any) error {
	switch value.(type) {
	case nil, bool, float64, string:
		return nil
	default:
		return errors.New("必须是标量值")
	}
}

func validateAdminUICondition(condition *NativePluginUICondition, manifest PluginManifest) error {
	switch condition.Op {
	case "eq", "ne", "truthy", "falsy", "nonempty", "empty", "in":
	default:
		return fmt.Errorf("不支持的条件操作符 %q", condition.Op)
	}
	if err := validateAdminUIBinding(condition.Path, false, manifest); err != nil {
		return err
	}
	if (condition.Op == "truthy" || condition.Op == "falsy" || condition.Op == "nonempty" || condition.Op == "empty") && condition.Value != nil {
		return errors.New("无参数条件不能包含 value")
	}
	if condition.Op == "in" {
		values, ok := condition.Value.([]any)
		if !ok || len(values) == 0 || len(values) > PluginAdminUIMaxOptions {
			return errors.New("in 条件 value 必须是有限数组")
		}
		for _, value := range values {
			if err := validateAdminUIScalar(value); err != nil {
				return err
			}
		}
	} else if condition.Op == "eq" || condition.Op == "ne" {
		if err := validateAdminUIScalar(condition.Value); err != nil {
			return err
		}
	}
	return nil
}

func validateAdminUILocalBinding(path string, manifest PluginManifest) error {
	if err := validateAdminUIBinding(path, true, manifest); err != nil {
		return err
	}
	return requireAdminUIBindingRoot(path, "local")
}

func requireAdminUIBindingRoot(path, expected string) error {
	segments, err := parseAdminUIJSONPointer(path)
	if err != nil {
		return err
	}
	if len(segments) < 2 || segments[0] != expected {
		return fmt.Errorf("绑定路径必须以 /%s/ 开头", expected)
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
	case "config", "resources", "status", "local", "item", "secrets":
	default:
		return errors.New("绑定根必须是 config、resources、status、local、item 或 secrets")
	}
	if writable && segments[0] != "config" && segments[0] != "local" {
		return errors.New("可写绑定只能指向 config 或 local")
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
	if segments[0] == "config" && len(manifest.ConfigSecrets) > 0 && len(segments) == 1 {
		return errors.New("绑定路径不能读取包含宿主秘密状态的 config 根对象")
	}
	if segments[0] == "config" {
		for _, secret := range manifest.ConfigSecrets {
			if len(segments) > 1 && segments[1] == secret {
				return errors.New("绑定路径不能访问插件秘密字段")
			}
		}
	}
	if segments[0] == "secrets" {
		if writable || len(segments) != 2 {
			return errors.New("secrets 绑定只能读取单个配置状态")
		}
		allowed := false
		for _, secret := range manifest.ConfigSecrets {
			if segments[1] == secret {
				allowed = true
				break
			}
		}
		if !allowed {
			return errors.New("secrets 绑定字段未在 manifest 声明")
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
	if value == "__proto__" || value == "prototype" || value == "constructor" {
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

// Property identifiers are used for row keys, filter keys and action object
// fields. Keep the same conservative alphabet as node/action identifiers while
// rejecting prototype-pollution names explicitly.
func isSafeAdminUIPropertyIdentifier(value string) bool {
	return isSafeAdminUIIdentifier(value)
}

func isWritableAdminUINode(nodeType string) bool {
	switch nodeType {
	case "text_input", "number_input", "switch", "select", "multiselect", "search", "checkbox":
		return true
	default:
		return false
	}
}
