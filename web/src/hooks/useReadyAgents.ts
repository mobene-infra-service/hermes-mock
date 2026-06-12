import { useSyncExternalStore } from 'react'

// 跨页共享「前端软电话已就绪（sipReady）的坐席号」集合。
// 常驻单例 AgentSoftphone 写入（setReadyAgents），群呼页坐席分配下拉订阅读取（已就绪默认排前）。
// 纯前端运行态，不落后端——就绪是浏览器 jssip 注册 + call-center SIP-ready 的实时结果。
type Listener = () => void

let ready: string[] = []
const listeners = new Set<Listener>()

// setReadyAgents 更新就绪坐席号集合；仅在内容变化时换引用并通知，避免无谓重渲染（useSyncExternalStore 要求快照引用稳定）。
export function setReadyAgents(nums: string[]): void {
  const next = Array.from(new Set(nums)).sort()
  if (next.length === ready.length && next.every((n, i) => n === ready[i])) return
  ready = next
  listeners.forEach((l) => l())
}

function subscribe(l: Listener): () => void {
  listeners.add(l)
  return () => { listeners.delete(l) }
}

function getSnapshot(): string[] { return ready }

// useReadyAgents 订阅就绪坐席号集合（React 18 外部 store）。
export function useReadyAgents(): string[] {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
