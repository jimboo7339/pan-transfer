// 分享链接的文件夹定位（与 CASX getShareurl / extractShareFid 对齐）。
// 预览里「只转存当前文件夹」把文件夹写入链接自身：#/list/share/<fid>（或 ?fid=），
// 因此多链接任务里每条链接各自记住自己的文件夹，互不影响。

const ROOT_FIDS = new Set(['', '0', 'root'])

// extractShareFid 从分享链接中取出文件夹 fid；没有则返回空串。
export function extractShareFid(url) {
  const s = String(url || '').trim()
  if (!s) return ''
  const mq = s.match(/(?:\?|&)(?:fid|fileId)=([^&#]+)/)
  if (mq?.[1]) {
    const v = decodeURIComponent(String(mq[1]).trim())
    if (!ROOT_FIDS.has(v.toLowerCase())) return v
  }
  const m1 = s.match(/#\/list\/share\/([A-Za-z0-9_-]{6,64})/)
  if (m1?.[1]) return m1[1]
  const m2 = s.match(/\/([a-fA-F0-9]{32})-?[^/]*$/)
  if (m2?.[1]) return m2[1]
  return ''
}

// withShareFid 把文件夹 fid 写入链接（fid 为空 = 恢复为转存根目录）。
export function withShareFid(url, fid) {
  const raw = String(url || '').trim()
  const base = stripFidParam(raw.split('#')[0])
  const id = String(fid || '').trim()
  if (ROOT_FIDS.has(id.toLowerCase())) return base
  return `${base}#/list/share/${encodeURIComponent(id)}`
}

// stripShareFid 去掉链接里的文件夹定位，回到链接根目录。
export function stripShareFid(url) {
  return withShareFid(url, '')
}

// stripFidParam 去掉 query 里已有的 fid / fileId 参数，避免与 fragment 冲突。
function stripFidParam(base) {
  const i = base.indexOf('?')
  if (i < 0) return base
  const head = base.slice(0, i)
  const kept = base
    .slice(i + 1)
    .split('&')
    .filter((kv) => {
      if (!kv) return false
      const k = kv.split('=')[0]
      return k !== 'fid' && k !== 'fileId'
    })
  return kept.length ? `${head}?${kept.join('&')}` : head
}