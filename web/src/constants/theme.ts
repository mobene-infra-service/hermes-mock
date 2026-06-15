// 设计系统色板（对照 Figma「hermes-mock · 通话测试场景」重构稿）。
// Tailwind slate + blue 体系：深色侧边栏 #0f172a、主色 #2563eb、浅色内容区 #f5f7fa。
// 这里集中导出色值常量 + Antd ConfigProvider 主题对象，页面与 CSS 单一来源。
export const C = {
  // 主色 / 语义色
  primary: '#2563eb',
  primaryHover: '#1d4ed8',
  success: '#16a34a',
  successBg: '#ecfdf3',
  successText: '#15803d',
  danger: '#dc2626',
  dangerBg: '#fef2f2',
  dangerText: '#b91c1c',
  warning: '#d97706',
  warningBg: '#fffbeb',
  info: '#2563eb',
  infoBg: '#eff4ff',
  infoText: '#1e3a8a',

  // 文本
  text: '#0f172a',
  textSecondary: '#475569',
  textMuted: '#64748b',
  textFaint: '#94a3b8',

  // 表面 / 边框
  bg: '#f5f7fa',
  surface: '#ffffff',
  surfaceAlt: '#f8fafc',
  border: '#e5e9f0',
  borderLight: '#eef1f6',

  // 侧边栏（深色）
  sidebar: '#0f172a',
  sidebarText: '#cbd5e1',
  sidebarIcon: '#64748b',
  sidebarGroupLabel: '#64748b',
  sidebarActive: '#2563eb',
} as const

// Antd 5 主题 token —— 与上面的色板保持一致，确保所有 antd 组件自动套用新视觉。
export const antdTheme = {
  token: {
    colorPrimary: C.primary,
    colorSuccess: C.success,
    colorError: C.danger,
    colorWarning: C.warning,
    colorInfo: C.info,
    colorTextBase: C.text,
    colorBgLayout: C.bg,
    colorBorder: C.border,
    colorBorderSecondary: C.borderLight,
    borderRadius: 8,
    fontFamily:
      "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', 'Helvetica Neue', sans-serif",
    fontSize: 14,
  },
  components: {
    Layout: { headerHeight: 60, headerPadding: '0 28px', bodyBg: C.bg, headerBg: C.surface },
    Card: { borderRadiusLG: 12, headerFontSize: 15, boxShadowTertiary: '0 1px 3px rgba(15,23,42,0.06)' },
    Table: { headerBg: C.surfaceAlt, headerColor: C.textMuted, borderColor: C.borderLight, cellPaddingBlockSM: 10 },
    Button: { borderRadius: 8, controlHeight: 36 },
    Tag: { borderRadiusSM: 6 },
    Segmented: { borderRadius: 8, itemSelectedBg: C.surface },
    Input: { borderRadius: 8 },
    Select: { borderRadius: 8 },
  },
}
