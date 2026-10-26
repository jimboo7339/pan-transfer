// 前端 cron 表达式辅助：常用预设 + 人性化文本。
export const CRON_PRESETS = [
  { label: '每 2 小时', value: '0 */2 * * *' },
  { label: '每 3 小时', value: '0 */3 * * *' },
  { label: '每天 08:00', value: '0 8 * * *' },
  { label: '每天 20:30', value: '30 20 * * *' },
  { label: '工作日 20:30', value: '30 20 * * 1-5' },
  { label: '周末 10:00', value: '0 10 * * 0,6' },
]

const WEEK_CN = ['日', '一', '二', '三', '四', '五', '六']

// 常见 cron 表达式转中文；无法解析就原样返回。
export function cronToText(expr) {
  if (!expr) return ''
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5) return expr
  const [min, hour, dom, mon, dow] = parts
  const hhmm = (h, m) => `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`

  // 每 N 小时
  const hourStep = hour.match(/^(\d+)\/(\d+)$/)
  if (min === '0' && hourStep) return `每 ${hourStep[2]} 小时`
  if (min === '30' && hourStep) return `每 ${hourStep[2]} 小时（半点）`
  if (min === '*' && hour === '*' && dom === '*' && mon === '*' && dow === '*') return '每分钟'
  // 固定点 每天
  if (/^\d+$/.test(hour) && /^\d+$/.test(min) && dom === '*' && mon === '*' && dow === '*') {
    return `每天 ${hhmm(hour, min)}`
  }
  // 固定点 特定周几
  if (/^\d+$/.test(hour) && /^\d+$/.test(min) && dom === '*' && mon === '*' && /^[0-6,\-]+$/.test(dow)) {
    const days = dow.split(',').map(d => {
      const [a, b] = d.split('-')
      if (a === b) return WEEK_CN[+a]
      let out = []
      for (let i = +a; i <= +b; i++) out.push(WEEK_CN[i % 7])
      return out.join('–')
    })
    return `每周${days.join('、')} ${hhmm(hour, min)}`
  }
  // 每月
  if (/^\d+$/.test(dom) && /^\d+$/.test(hour) && /^\d+$/.test(min) && mon === '*' && dow === '*') {
    return `每月 ${dom} 日 ${hhmm(hour, min)}`
  }
  return expr
}
