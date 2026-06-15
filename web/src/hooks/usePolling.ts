import { useEffect, useRef } from 'react'

export interface PollingOptions {
  // false 时完全不轮询（用于「无进行中任务则停」「终态则停」等条件控制）。默认 true。
  enabled?: boolean
  // 启用时是否立即执行一次（默认 true）。
  immediate?: boolean
  // 额外可见性判断：返回 false 时跳过本次 tick（如常驻 display:none 的容器查 offsetParent）。
  isVisible?: () => boolean
}

// usePolling —— 受控周期轮询，统一各页自动刷新行为：
//  · 标签页隐藏（document.hidden）时自动跳过本次请求，回到前台恢复，杜绝后台空轮询；
//  · fn 用 ref 镜像：每轮都取最新闭包（读到最新 state/props），但不会因 fn 重建而重置计时；
//  · enabled / intervalMs 变化才重建定时器；enabled=false 即停（条件停止/终态停止）。
export function usePolling(
  fn: () => void | Promise<void>,
  intervalMs: number,
  opts: PollingOptions = {},
) {
  const { enabled = true, immediate = true, isVisible } = opts
  const fnRef = useRef(fn)
  fnRef.current = fn
  const visRef = useRef(isVisible)
  visRef.current = isVisible

  useEffect(() => {
    if (!enabled) return
    let alive = true
    const tick = () => {
      if (!alive || document.hidden) return
      if (visRef.current && !visRef.current()) return
      void fnRef.current()
    }
    if (immediate) tick()
    const t = setInterval(tick, intervalMs)
    return () => { alive = false; clearInterval(t) }
  }, [enabled, intervalMs, immediate])
}
