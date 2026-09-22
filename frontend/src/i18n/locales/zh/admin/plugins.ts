export default {
  plugins: {
    title: '插件管理',
    description: '安装和管理独立运行的 OAuth 出站传输插件。API Key 流程不受影响。',
    upload: '安装插件',
    upgrade: '升级',
    upgradeSuccess: '插件升级成功',
    minimumSDK: '宿主最低能力',
    sdkRequirements: '协议 {plugin_protocol} / 传输 {transport_api} / UI Bridge {ui_bridge}',
    actionUnavailable: '插件运行时才可执行操作',
    bridgeRequestFailed: '插件请求失败，请刷新状态后再重试。',
    secretsTitle: '{name} 凭据',
    secretConfigured: '已配置',
    secretNotConfigured: '未配置',
    secretOperation: '操作',
    secretKeep: '保留当前值',
    secretReplace: '替换',
    secretClear: '清空',
    secretsLoadFailed: '无法读取凭据配置状态，请关闭弹窗后重试。',
    secretsSaveFailed: '凭据保存失败，请关闭弹窗并核对当前状态后再重试。',
    uploadHint: '仅接受 .s2plugin 包；默认要求可信发布者签名。',
    runtimeNotice: '插件安装、启用、停用和配置由 Sub2API 宿主动态处理，通常不需要重启宿主实例。只有宿主版本或宿主配置本身变化时，才按部署方式执行重启。',
    menuNotice: '系统设置中的“插件管理”开关仅控制侧边栏菜单显示，不会停止已经加载或正在运行的插件。',
    empty: '尚未安装插件',
    emptyHint: '选择本机的 .s2plugin 文件进行安装。Sub2API 不会自动下载第三方插件。',
    configure: '配置',
    enable: '启用',
    disable: '停用',
    test: '测试',
    uninstall: '卸载',
    rollout: 'OAuth 流量比例',
    compatibility: '版本兼容性',
    currentVersion: '当前 Sub2API',
    requiredVersion: '要求范围',
    recommendedVersion: '建议版本',
    signature: '包签名',
    trusted: '已验证',
    unsigned: '未签名',
    runtime: '运行状态',
    healthy: '运行正常',
    unhealthy: '未运行',
    compatible: '兼容',
    untested: '未验证版本',
    incompatible: '不兼容',
    enabled: '已启用',
    disabled: '已停用',
    error: '异常',
    starting: '启动中',
    configTitle: '{name} 配置',
    loadingUI: '正在加载插件配置界面...',
    uiUnavailable: '无法加载插件配置界面',
    uploadSuccess: '插件安装成功，当前保持停用',
    enableSuccess: '插件已启用',
    disableSuccess: '插件已停用',
    uninstallSuccess: '插件已卸载',
    testSuccess: '插件测试通过',
    confirmDisable: '确定停用此插件吗？新的 OAuth 请求会立即恢复 Sub2API 原有路径。',
    confirmUninstall: '确定卸载此插件吗？插件必须先停用。此操作会移除安装文件和配置。',
    confirmUntested: '该插件兼容当前版本范围，但未声明已测试当前 Sub2API 版本。确定承担风险并启用吗？',
    fileRequired: '请选择 .s2plugin 文件',
    bridgeRejected: '插件 UI 消息校验失败',
    onlyOpenAI: '初期能力：仅 OpenAI OAuth 出站传输',
    noAccountCoupling: '插件配置独立管理，账号凭据保持隔离。'
    ,nativePageTitle: '插件页面'
    ,nativePageDescription: '由宿主安全渲染的插件管理页面。'
    ,nativePageLoadFailed: '插件页面加载失败，请返回插件管理后重试。'
    ,nativePageNotFound: '插件不存在或已卸载。'
    ,nativePageUnavailable: '该插件没有可用的原生管理页面。'
    ,backToList: '返回插件管理'
    ,unsavedNativeConfig: '有未保存的插件配置修改。'
    ,nativeActionConfirm: '确定要执行此操作吗？'
    ,leaveNativePageConfirm: '插件配置有未保存的修改，确定要离开此页面吗？'
    ,lastUpdated: '最近刷新'
    ,statusStale: '状态可能已过期，请刷新确认'
    ,actionRunning: '操作执行中'
    ,validationFailed: '请先修正以下配置项'
    ,actionAccepted: '操作已提交'
    ,selectPage: '选择当前页'
    ,selectFiltered: '选择全部筛选结果'
    ,clearSelection: '清空选择'
    ,selectedRows: '项已选'
    ,hiddenSelected: '项不在当前结果中'
    ,noMatchingRows: '没有匹配记录'
    ,select: '选择'
    ,toggleDetails: '展开或收起详情'
    ,actions: '操作'
    ,rows: '条'
    ,previous: '上一页'
    ,next: '下一页'
  }
}
