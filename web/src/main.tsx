import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import './antd-reset.css'
import './index.css'
import App from './App'
import { antdTheme } from './constants/theme'

// dayjs 中文与日期插件（Antd DatePicker 依赖）
import dayjs from 'dayjs'
import customParseFormat from 'dayjs/plugin/customParseFormat'
import weekday from 'dayjs/plugin/weekday'
import localeData from 'dayjs/plugin/localeData'
import 'dayjs/locale/zh-cn'

dayjs.extend(customParseFormat)
dayjs.extend(weekday)
dayjs.extend(localeData)
dayjs.locale('zh-cn')

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={antdTheme}
    >
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </ConfigProvider>
  </React.StrictMode>,
)
