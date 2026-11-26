package transfer

// 同目标名冲突时的去重策略。
//
// 历史行为是「保留最大」（按文件大小、再按更新时间），始终保留为默认值；
// 「优先清晰度」在比较大小之前先按文件名里的分辨率标记分档，忽略大小写。
// 分档相同时自然回退到大小/时间，避免两种策略出现行为漂移。

import (
	"regexp"
	"strings"
)

const (
	DedupLargest = "largest" // 保留最大（默认）
	DedupQuality = "quality" // 优先清晰度
)

// normalizeDedupStrategy 把空值与未知值归一为默认策略（保留最大）。
func normalizeDedupStrategy(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), DedupQuality) {
		return DedupQuality
	}
	return DedupLargest
}

// resolutionTierRegexps 按档位从高到低排列。
//
// 两侧都留了非字母数字边界，避免 1080p 命中更长数字串的一部分；
// 右侧额外允许数字，兼容 2160p60fps 这类没有分隔符连写的写法。
// (?i) 统一忽略大小写，所以 2160P / 4k / 4K / UHD 都能识别。
var resolutionTierRegexps = []struct {
	tier int
	re   *regexp.Regexp
}{
	{4, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:2160p|4k|uhd)(?:[^a-z0-9]|[0-9]|$)`)},
	{3, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:1440p|2k)(?:[^a-z0-9]|[0-9]|$)`)},
	{2, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:1080p|1080i)(?:[^a-z0-9]|[0-9]|$)`)},
	{1, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:720p|720i)(?:[^a-z0-9]|[0-9]|$)`)},
}

// resolutionTier 返回文件名中的最高清晰度档位；没有明确标记时为 0（最低）。
func resolutionTier(name string) int {
	for _, t := range resolutionTierRegexps {
		if t.re.MatchString(name) {
			return t.tier
		}
	}
	return 0
}

// DedupCandidate 是冲突比较所需的最小信息。
// preview 与 executor 各自有本地 candidate 类型，这里统一成公共结构，
// 保证两处（预览标注 / 实际转存）的胜者判定完全一致。
type DedupCandidate struct {
	Name      string // 原始文件名，用于清晰度识别
	Size      int64
	UpdatedAt int64
}

// betterDedupCandidate 返回 cur 是否应该取代 prev 成为当前分组的胜者。
func betterDedupCandidate(cur, prev DedupCandidate, strategy string) bool {
	if normalizeDedupStrategy(strategy) == DedupQuality {
		cq, pq := resolutionTier(cur.Name), resolutionTier(prev.Name)
		if cq != pq {
			return cq > pq
		}
	}
	if cur.Size != prev.Size {
		return cur.Size > prev.Size
	}
	return cur.UpdatedAt > prev.UpdatedAt
}

// conflictLabel 返回冲突行在预览里的标注文案。
func conflictLabel(strategy string) string {
	if normalizeDedupStrategy(strategy) == DedupQuality {
		return "重命名冲突（优先清晰度）"
	}
	return "重命名冲突（保留最大）"
}
